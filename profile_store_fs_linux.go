package main

// All store operations are relative to verified, no-follow directory FDs.
// Never re-resolve an already checked ancestor through its pathname.
import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type profileStoreDir struct {
	file *os.File
	name string
}

func openProfileStoreDir(path string) (*profileStoreDir, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid profile store path")
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, fmt.Errorf("invalid profile store path")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("profile store directory unavailable")
	}
	current := os.NewFile(uintptr(fd), "profile store directory")
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err == syscall.ENOENT {
			err = syscall.Mkdirat(int(current.Fd()), part, 0700)
			if err == nil || err == syscall.EEXIST {
				next, err = syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			current.Close()
			return nil, fmt.Errorf("profile store ancestor unavailable or unsafe")
		}
		child := os.NewFile(uintptr(next), "profile store directory")
		current.Close()
		current = child
		var st syscall.Stat_t
		if syscall.Fstat(next, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0)) {
			current.Close()
			return nil, fmt.Errorf("profile store ancestor must be a trusted directory")
		}
	}
	// The private parent itself must be ours, even when trusted root ancestors
	// or a root-owned sticky temporary directory were allowed along the way.
	var st syscall.Stat_t
	if syscall.Fstat(int(current.Fd()), &st) != nil || st.Uid != uint32(os.Geteuid()) {
		current.Close()
		return nil, fmt.Errorf("profile store directory must be owned")
	}
	return &profileStoreDir{current, parts[len(parts)-1]}, nil
}

func (d *profileStoreDir) open(name string) (*os.File, error) {
	fd, err := syscall.Openat(int(d.file.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "profile store file")
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, fmt.Errorf("profile store must be a private owned regular file")
	}
	if st.Size > maxProfileBytes {
		f.Close()
		return nil, fmt.Errorf("profile store exceeds size limit")
	}
	return f, nil
}

func boundedProfileRead(f *os.File) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("profile store could not be read")
	}
	if len(b) > maxProfileBytes {
		return nil, fmt.Errorf("profile store exceeds size limit")
	}
	return b, nil
}

// A nil before is creation only, never permission to replace an existing file.
func (d *profileStoreDir) checkTarget(before *Settings) error {
	f, err := d.open(d.name)
	if os.IsNotExist(err) && before == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("profile store target unavailable or unsafe")
	}
	defer f.Close()
	if before == nil {
		return fmt.Errorf("profile store appeared during initialization")
	}
	b, err := boundedProfileRead(f)
	if err != nil {
		return err
	}
	return checkCurrentProfileDocument(b, *before)
}

func (d *profileStoreDir) write(b []byte, before *Settings) error {
	// Check the opened current document before permission or durable changes.
	if err := d.checkTarget(before); err != nil {
		return err
	}
	if err := d.file.Chmod(0700); err != nil {
		return fmt.Errorf("profile store directory permissions could not be secured")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("profile store temporary name failed")
	}
	name := ".profiles-" + hex.EncodeToString(nonce[:])
	fd, err := syscall.Openat(int(d.file.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("profile store write failed")
	}
	defer syscall.Unlinkat(int(d.file.Fd()), name)
	f := os.NewFile(uintptr(fd), "profile store temporary file")
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		f.Close()
		return fmt.Errorf("profile store temporary file unsafe")
	}
	err = f.Chmod(0600)
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		// Recheck after staging to detect ordinary intervening external edits.
		// This is not cross-process CAS: a privileged writer can still race
		// this check and rename. Same-process transactions hold profileMutex.
		err = d.checkTarget(before)
	}
	if err == nil {
		err = syscall.Renameat(int(d.file.Fd()), name, int(d.file.Fd()), d.name)
	}
	if err != nil {
		return fmt.Errorf("profile store atomic write failed")
	}
	if err = d.file.Sync(); err != nil {
		return fmt.Errorf("profile store directory sync failed")
	}
	return nil
}
