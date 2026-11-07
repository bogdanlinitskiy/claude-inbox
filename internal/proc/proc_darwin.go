//go:build darwin

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

// psArgs makes ps print what parsePS reads: the arguments come last because they may hold spaces.
var psArgs = []string{"-o", "pid=,ppid=,lstart=,args="}

// FS reads processes from ps, since macOS has no /proc.
type FS struct{}

// New ignores root, which only Linux has; it keeps one signature for both systems.
func New(string) FS {
	return FS{}
}

// FindAncestor returns the first process named name among pid and its ancestors, looking at most maxDepth
// processes up. One ps call lists them all, so a hook pays for one process start.
func (FS) FindAncestor(pid int, name string, maxDepth int) (Process, bool) {
	table, err := ps("-A")
	if err != nil {
		return Process{}, false
	}
	return ancestorIn(table, pid, name, maxDepth)
}

// Alive reports whether p still runs: its pid exists and started at the same second.
func (FS) Alive(p Process) bool {
	table, err := ps("-p", strconv.Itoa(p.PID))
	if err != nil {
		return false
	}
	e, ok := table[p.PID]
	return ok && e.start == p.Start
}

// ps runs ps in the C locale, whose lstart parsePS reads. ps exits 1 for a pid that does not exist.
func ps(selection ...string) (map[int]psEntry, error) {
	cmd := exec.Command("ps", append(selection, psArgs...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePS(out, time.Local)
}
