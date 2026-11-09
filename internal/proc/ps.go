package proc

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// lstartLayout is ps's lstart column in the C locale, e.g. "Sat Nov  7 13:04:01 2026".
const (
	lstartLayout = "Mon Jan _2 15:04:05 2006"
	lstartFields = 5
)

type psEntry struct {
	ppid  int
	start uint64
	args  string
}

// parsePS reads ps lines of pid, ppid, lstart and args into a table by pid. The start time becomes Unix seconds
// (ps shows no finer), read in loc, which ps uses too.
func parsePS(out []byte, loc *time.Location) (map[int]psEntry, error) {
	table := map[int]psEntry{}
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2+lstartFields {
			return nil, fmt.Errorf("parse ps line %q: too few fields", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("parse ps pid %q: %w", fields[0], err)
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse ps ppid %q: %w", fields[1], err)
		}
		started, err := time.ParseInLocation(lstartLayout, strings.Join(fields[2:2+lstartFields], " "), loc)
		if err != nil {
			return nil, fmt.Errorf("parse ps start of %d: %w", pid, err)
		}
		//nolint:gosec // a process start time is never before 1970, so the conversion cannot go negative.
		table[pid] = psEntry{ppid: ppid, start: uint64(started.Unix()), args: strings.Join(fields[2+lstartFields:], " ")}
	}
	return table, nil
}

// named reports whether the process runs the command name: its first argument is name or a path ending in it.
// Claude Code names its process "claude" there.
func (e psEntry) named(name string) bool {
	first, _, _ := strings.Cut(e.args, " ")
	return filepath.Base(first) == name
}

// ancestorIn walks the table up from pid, as FindAncestor does on /proc.
func ancestorIn(table map[int]psEntry, pid int, name string, maxDepth int) (Process, bool) {
	for range maxDepth {
		if pid <= 1 {
			return Process{}, false
		}
		e, ok := table[pid]
		if !ok {
			return Process{}, false
		}
		if e.named(name) {
			return Process{PID: pid, Start: e.start}, true
		}
		pid = e.ppid
	}
	return Process{}, false
}
