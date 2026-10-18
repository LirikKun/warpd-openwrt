package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const relayStagePrefix = "warpd-relay-account-"

// relayStageRoot and relayStage keep every operation relative to verified FDs.
// Pathnames are used only once to resolve the configured root, with no-follow
// openat traversal; later ancestor renames cannot redirect staging or cleanup.
type relayStageRoot struct{ dir *os.File }
type relayStage struct {
	root *relayStageRoot
	dir  *os.File
	name string
}

func openRelayStageRoot(path string) (*relayStageRoot, error) {
	abs, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(abs) {
		return nil, errors.New("unsafe Relay staging root")
	}
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("unsafe Relay staging root")
	}
	current := os.NewFile(uintptr(fd), "Relay staging root traversal")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			current.Close()
			return nil, errors.New("unsafe Relay staging root")
		}
		next, e := syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if e != nil {
			current.Close()
			return nil, errors.New("unsafe Relay staging root")
		}
		child := os.NewFile(uintptr(next), "Relay staging root traversal")
		var st syscall.Stat_t
		if syscall.Fstat(next, &st) != nil || !trustedRelayStageAncestor(&st) {
			child.Close()
			current.Close()
			return nil, errors.New("unsafe Relay staging root")
		}
		current.Close()
		current = child
	}
	var st syscall.Stat_t
	if syscall.Fstat(int(current.Fd()), &st) != nil || !trustedRelayStageRoot(&st) {
		current.Close()
		return nil, errors.New("unsafe Relay staging root")
	}
	return &relayStageRoot{dir: current}, nil
}

func trustedRelayStageAncestor(st *syscall.Stat_t) bool {
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
		return false
	}
	return st.Mode&0022 == 0 || (st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0)
}
func trustedRelayStageRoot(st *syscall.Stat_t) bool {
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return false
	}
	if st.Uid == uint32(os.Geteuid()) {
		return st.Mode&0022 == 0 || (st.Mode&syscall.S_ISVTX != 0 && st.Mode&0022 != 0)
	}
	return st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0 && st.Mode&0022 != 0
}

func (r *relayStageRoot) create() (*relayStage, error) {
	if r == nil || r.dir == nil {
		return nil, errors.New("Relay staging unavailable")
	}
	// A leftover directory means earlier secret cleanup was incomplete. Refuse
	// another registration until an operator removes/reviews that private data.
	scanFD, err := syscall.Openat(int(r.dir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("Relay staging unavailable")
	}
	scan := os.NewFile(uintptr(scanFD), "Relay staging scan")
	names, err := scan.Readdirnames(-1)
	closeErr := scan.Close()
	if err != nil || closeErr != nil {
		return nil, errors.New("Relay staging unavailable")
	}
	for _, name := range names {
		if strings.HasPrefix(name, relayStagePrefix) {
			return nil, errors.New("Relay staging cleanup is pending")
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, errors.New("Relay staging unavailable")
		}
		name := relayStagePrefix + hex.EncodeToString(nonce[:])
		if err = syscall.Mkdirat(int(r.dir.Fd()), name, 0700); err == syscall.EEXIST {
			continue
		} else if err != nil {
			return nil, errors.New("Relay staging unavailable")
		}
		fd, openErr := syscall.Openat(int(r.dir.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if openErr != nil {
			_ = unlinkatDir(int(r.dir.Fd()), name)
			return nil, errors.New("Relay staging unavailable")
		}
		dir := os.NewFile(uintptr(fd), "Relay account staging")
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != uint32(os.Geteuid()) || syscall.Fchmod(fd, 0700) != nil {
			dir.Close()
			_ = unlinkatDir(int(r.dir.Fd()), name)
			return nil, errors.New("Relay staging unavailable")
		}
		return &relayStage{root: r, dir: dir, name: name}, nil
	}
	return nil, errors.New("Relay staging unavailable")
}

func (s *relayStage) childDir() string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), s.dir.Fd())
}
func (s *relayStage) createOutput() error {
	fd, err := syscall.Openat(int(s.dir.Fd()), "account.json", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("Relay credential staging unavailable")
	}
	f := os.NewFile(uintptr(fd), "Relay credential staging output")
	var st syscall.Stat_t
	ok := syscall.Fstat(fd, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Mode&0777 == 0600 && st.Nlink == 1 && st.Uid == uint32(os.Geteuid())
	if ok {
		ok = f.Chmod(0600) == nil
	}
	closeErr := f.Close()
	if !ok || closeErr != nil {
		_ = unlinkatEntry(int(s.dir.Fd()), "account.json", 0)
		return errors.New("Relay credential staging unavailable")
	}
	return nil
}

func (s *relayStage) openOutput() (*os.File, error) {
	fd, err := syscall.Openat(int(s.dir.Fd()), "account.json", syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("Relay credential output unavailable")
	}
	out := os.NewFile(uintptr(fd), "Relay credential output")
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Size > 64<<10 {
		out.Close()
		return nil, errors.New("Relay credential output unsafe")
	}
	return out, nil
}
func (s *relayStage) cleanup() error {
	if s == nil || s.root == nil || s.dir == nil {
		return errors.New("Relay staging cleanup failed")
	}
	// The generator's sole output is this regular file. Never follow a child
	// symlink; unlinkat removes the directory entry itself.
	if err := syscall.Unlinkat(int(s.dir.Fd()), "account.json"); err != nil && err != syscall.ENOENT {
		return errors.New("Relay staging cleanup failed")
	}
	if err := s.dir.Close(); err != nil {
		return errors.New("Relay staging cleanup failed")
	}
	if err := unlinkatDir(int(s.root.dir.Fd()), s.name); err != nil {
		return errors.New("Relay staging cleanup failed")
	}
	return nil
}
func unlinkatDir(dirfd int, name string) error {
	return unlinkatEntry(dirfd, name, 0x200)
}
func unlinkatEntry(dirfd int, name string, flags uintptr) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), flags)
	if errno != 0 {
		return errno
	}
	return nil
}
