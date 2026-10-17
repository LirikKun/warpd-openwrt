package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Only addresses/ports and PIDs belong in ownership errors, never YAML/keys.
type mihomoListener struct {
	address string
	port    int
}

type mihomoSocket struct {
	mihomoListener
	inode string
}

// Read the listeners from our renderer, so omitted/invalid chains cannot drift
// from the listener set used by supervision. No arbitrary YAML is accepted here.
func mihomoListeners(conf string) ([]mihomoListener, error) {
	listeners := []mihomoListener{{address: "127.0.0.1", port: 9099}}
	address := ""
	inListeners := false
	for _, line := range strings.Split(conf, "\n") {
		if line == "listeners:" {
			inListeners = true
			continue
		}
		if !inListeners {
			continue
		}
		if strings.HasPrefix(line, "    listen: ") {
			var err error
			address, err = strconv.Unquote(strings.TrimPrefix(line, "    listen: "))
			if err != nil {
				return nil, fmt.Errorf("invalid mihomo listener address")
			}
		}
		if strings.HasPrefix(line, "    port: ") {
			port, err := strconv.Atoi(strings.TrimPrefix(line, "    port: "))
			if err != nil || port < 1 || port > 65535 || net.ParseIP(address) == nil {
				return nil, fmt.Errorf("invalid mihomo listener address or port")
			}
			listeners = append(listeners, mihomoListener{address, port})
		}
	}
	return listeners, nil
}

// /proc/net belongs to the caller's network namespace. PID fd links, not a
// matching executable name (shared with SSClash), establish ownership.
func readMihomoSockets(root string) ([]mihomoSocket, error) {
	var sockets []mihomoSocket
	for _, table := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(root, "net", table))
		if os.IsNotExist(err) && table == "tcp6" {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read mihomo listener table: %w", err)
		}
		scanner := bufio.NewScanner(f)
		if !scanner.Scan() || !strings.Contains(scanner.Text(), "local_address") {
			f.Close()
			return nil, fmt.Errorf("invalid mihomo listener table header")
		}
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				f.Close()
				return nil, fmt.Errorf("invalid mihomo listener table")
			}
			if fields[3] != "0A" {
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) != 2 {
				f.Close()
				return nil, fmt.Errorf("invalid mihomo listener table")
			}
			raw, err := hex.DecodeString(parts[0])
			if err != nil || (len(raw) != 4 && len(raw) != 16) {
				f.Close()
				return nil, fmt.Errorf("invalid mihomo listener table")
			}
			// Linux presents each 32-bit address word in host byte order.
			for i := 0; i < len(raw); i += 4 {
				binary.BigEndian.PutUint32(raw[i:i+4], binary.NativeEndian.Uint32(raw[i:i+4]))
			}
			port, err := strconv.ParseUint(parts[1], 16, 16)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("invalid mihomo listener table")
			}
			sockets = append(sockets, mihomoSocket{mihomoListener{net.IP(raw).String(), int(port)}, fields[9]})
		}
		err = scanner.Err()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot read mihomo listener table: %w", err)
		}
	}
	return sockets, nil
}

func (m *MihomoMan) checkOwnership(pid int, listeners []mihomoListener) error {
	return m.inspectListeners(pid, listeners, true)
}

// Allow sockets already owned by our child, but reject other owners before
// writing a replacement config or sending any controller request.
func (m *MihomoMan) checkCollisions(pid int, listeners []mihomoListener) error {
	return m.inspectListeners(pid, listeners, false)
}

func (m *MihomoMan) inspectListeners(pid int, listeners []mihomoListener, require bool) error {
	if m.ownership != nil {
		return m.ownership(pid, listeners)
	}
	root := firstNonEmpty(m.procRoot, "/proc")
	sockets, err := readMihomoSockets(root)
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	if pid > 0 {
		dir := filepath.Join(root, strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("mihomo listener ownership unavailable for pid %d", pid)
		}
		for _, entry := range entries {
			link, err := os.Readlink(filepath.Join(dir, entry.Name()))
			if os.IsNotExist(err) {
				continue
			} // descriptors can close while sampled
			if err != nil {
				return fmt.Errorf("mihomo listener ownership unavailable for pid %d", pid)
			}
			if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
				owned[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
			}
		}
	}
	for _, want := range listeners {
		found := false
		dualStackInode := ""
		additionalBind := false
		for _, socket := range sockets {
			exact := net.ParseIP(socket.address).Equal(net.ParseIP(want.address))
			if socket.port == want.port && (exact || net.ParseIP(socket.address).IsUnspecified() || net.ParseIP(want.address).IsUnspecified()) {
				if pid == 0 || !owned[socket.inode] {
					return fmt.Errorf("mihomo listener collision at %s (not owned by pid %d)", net.JoinHostPort(want.address, strconv.Itoa(want.port)), pid)
				}
				if exact {
					found = true
				}
				if socket.address == "::" && want.address == "0.0.0.0" {
					dualStackInode = socket.inode
				} else if !exact {
					additionalBind = true
				}
			}
		}
		if require && pid > 0 && !found && dualStackInode != "" && !additionalBind {
			// /proc cannot expose IPV6_V6ONLY. A successful IPv4 probe is only
			// evidence for this owned :: socket if no other bind can answer it,
			// even when the other bind belongs to the same PID. No data is sent.
			conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(want.port)), 50*time.Millisecond)
			if err == nil {
				conn.Close()
				// Fail closed if the socket disappeared or an additional bind
				// appeared during the probe; proc sampling is not atomic.
				after, err := readMihomoSockets(root)
				if err != nil {
					return err
				}
				for _, socket := range after {
					if socket.port != want.port {
						continue
					}
					if socket.address != "::" || socket.inode != dualStackInode {
						found = false
						break
					}
					found = true
				}
			}
		}
		if require && pid > 0 && !found {
			return fmt.Errorf("mihomo listener ownership missing for pid %d at %s", pid, net.JoinHostPort(want.address, strconv.Itoa(want.port)))
		}
	}
	return nil
}
