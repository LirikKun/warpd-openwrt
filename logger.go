package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	stateDir   = "/var/run/warpd"
	account    = "/etc/warpd/warp-account.json"
	warpScout  = "/usr/bin/warpscout"
	workDir    = "/var/warpd"
	maxLogSize = 48 * 1024
)

// warpdLog is a tiny RAM ring-buffer logger: keeps N lines, never touches
// ROM, and flushes nothing to disk.
type warpdLog struct {
	mu   sync.Mutex
	max  int
	lines []string
	size int
}

var logg = &warpdLog{max: 300}

func (l *warpdLog) Printf(format string, args ...any) {
	line := time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	l.size += len(line)
	for l.size > maxLogSize && len(l.lines) > 1 {
		l.size -= len(l.lines[0])
		l.lines = l.lines[1:]
	}
}

func (l *warpdLog) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

func init() {
	_ = os.MkdirAll(stateDir, 0755)
	_ = os.MkdirAll(workDir, 0755)
}
