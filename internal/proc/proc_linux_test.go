//go:build linux

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	claudePID   = 100
	claudeStart = 5555
	shellPID    = 200
	hookPID     = 300
	maxDepth    = 5
)

// fakeProc writes /proc/<pid>/stat files for pid → (comm, ppid, start), in the kernel's format.
func fakeProc(t *testing.T, procs map[int]stat) FS {
	t.Helper()
	root := t.TempDir()
	for pid, s := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(dir, 0o700))
		line := fmt.Sprintf("%d (%s) S %d %d 0 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 %d 1000 100 18446744073709551615\n",
			pid, s.comm, s.ppid, pid, s.start)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o600))
	}
	return New(root)
}

func TestFindAncestor(t *testing.T) {
	cases := []struct {
		name  string
		procs map[int]stat
		from  int
		want  Process
		found bool
	}{
		{
			name: "through a shell",
			procs: map[int]stat{
				claudePID: {comm: "claude", ppid: 1, start: claudeStart},
				shellPID:  {comm: "sh", ppid: claudePID, start: 6000},
			},
			from: shellPID, want: Process{PID: claudePID, Start: claudeStart}, found: true,
		},
		{
			name:  "started directly",
			procs: map[int]stat{claudePID: {comm: "claude", ppid: 1, start: claudeStart}},
			from:  claudePID, want: Process{PID: claudePID, Start: claudeStart}, found: true,
		},
		{
			name: "no claude up to init",
			procs: map[int]stat{
				shellPID: {comm: "bash", ppid: 1, start: 6000},
			},
			from: shellPID,
		},
		{
			name: "deeper than the limit",
			procs: map[int]stat{
				10: {comm: "claude", ppid: 1, start: claudeStart}, 11: {comm: "a", ppid: 10}, 12: {comm: "b", ppid: 11},
				13: {comm: "c", ppid: 12}, 14: {comm: "d", ppid: 13}, 15: {comm: "e", ppid: 14},
			},
			from: 15,
		},
		{
			name:  "parent exited",
			procs: map[int]stat{hookPID: {comm: "claude-inbox", ppid: shellPID, start: 7000}},
			from:  hookPID,
		},
		{
			name:  "name with spaces and parentheses",
			procs: map[int]stat{
				claudePID: {comm: "claude", ppid: 1, start: claudeStart},
				shellPID:  {comm: "x) (y", ppid: claudePID, start: 6000},
			},
			from: shellPID, want: Process{PID: claudePID, Start: claudeStart}, found: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := fakeProc(t, tc.procs).FindAncestor(tc.from, "claude", maxDepth)
			assert.Equal(t, tc.found, found)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAlive(t *testing.T) {
	fs := fakeProc(t, map[int]stat{claudePID: {comm: "claude", ppid: 1, start: claudeStart}})

	assert.True(t, fs.Alive(Process{PID: claudePID, Start: claudeStart}))
	assert.False(t, fs.Alive(Process{PID: claudePID, Start: claudeStart + 1}), "a reused pid is another process")
	assert.False(t, fs.Alive(Process{PID: shellPID, Start: claudeStart}), "a missing pid is gone")
}

func TestAliveOnThisMachine(t *testing.T) {
	fs := New("/proc")
	self, err := fs.stat(os.Getpid())
	require.NoError(t, err)

	assert.True(t, fs.Alive(Process{PID: os.Getpid(), Start: self.start}))
}
