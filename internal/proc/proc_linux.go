//go:build linux

package proc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	// statPPID and statStart index the fields after the command name in /proc/<pid>/stat: field 3 (state) is
	// index 0, so field 4 (ppid) is 1 and field 22 (starttime) is 19.
	statPPID  = 1
	statStart = 19
)

// FS reads processes from the Linux /proc file system.
type FS struct {
	root string
}

// New reads processes from root, which is /proc outside tests.
func New(root string) FS {
	return FS{root: root}
}

// FindAncestor returns the first process named name among pid and its ancestors, looking at most maxDepth
// processes up. A process that exits during the walk ends it, as not found.
func (f FS) FindAncestor(pid int, name string, maxDepth int) (Process, bool) {
	for range maxDepth {
		if pid <= 1 {
			return Process{}, false
		}
		s, err := f.stat(pid)
		if err != nil {
			return Process{}, false
		}
		if s.comm == name {
			return Process{PID: pid, Start: s.start}, true
		}
		pid = s.ppid
	}
	return Process{}, false
}

// Alive reports whether p still runs: its pid exists and started at the same time.
func (f FS) Alive(p Process) bool {
	s, err := f.stat(p.PID)
	return err == nil && s.start == p.Start
}

type stat struct {
	comm  string
	ppid  int
	start uint64
}

// stat parses /proc/<pid>/stat. The command name sits in parentheses and may itself contain spaces and
// parentheses, so the fields after it are counted from the last ')'.
func (f FS) stat(pid int) (stat, error) {
	data, err := os.ReadFile(filepath.Join(f.root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return stat{}, fmt.Errorf("read stat of %d: %w", pid, err)
	}
	open := bytes.IndexByte(data, '(')
	closing := bytes.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return stat{}, fmt.Errorf("parse stat of %d: no command name", pid)
	}
	fields := bytes.Fields(data[closing+1:])
	if len(fields) <= statStart {
		return stat{}, fmt.Errorf("parse stat of %d: %d fields", pid, len(fields))
	}
	ppid, err := strconv.Atoi(string(fields[statPPID]))
	if err != nil {
		return stat{}, fmt.Errorf("parse ppid of %d: %w", pid, err)
	}
	start, err := strconv.ParseUint(string(fields[statStart]), 10, 64)
	if err != nil {
		return stat{}, fmt.Errorf("parse start time of %d: %w", pid, err)
	}
	return stat{comm: string(data[open+1 : closing]), ppid: ppid, start: start}, nil
}
