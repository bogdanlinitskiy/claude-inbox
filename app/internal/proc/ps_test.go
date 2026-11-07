package proc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// psOutput is ps -A -o pid=,ppid=,lstart=,args= as macOS prints it: a hook, the shell Claude Code ran it in, and
// claude (by path, and a second one by name) above it.
const psOutput = `    1     0 Sat Nov  7 09:00:00 2026     /sbin/launchd
  700     1 Sat Nov  7 10:00:00 2026     /Users/me/.local/bin/claude --resume abc
  701   700 Sat Nov  7 10:00:05 2026     /bin/zsh -c /Users/me/my tools/claude-inbox hook --discover
  702   701 Sat Nov  7 10:00:05 2026     /Users/me/my tools/claude-inbox hook --discover
  800     1 Sat Nov  7 11:00:00 2026     claude
`

func TestParsePS(t *testing.T) {
	table, err := parsePS([]byte(psOutput), time.UTC)

	require.NoError(t, err)
	require.Len(t, table, 5)
	assert.Equal(t, psEntry{ppid: 701, start: uint64(time.Date(2026, 11, 7, 10, 0, 5, 0, time.UTC).Unix()), args: "/Users/me/my tools/claude-inbox hook --discover"}, table[702])
}

func TestParsePSRefusesWhatItCannotRead(t *testing.T) {
	for name, out := range map[string]string{
		"short line":  "  12     1 Sat Nov  7\n",
		"bad pid":     "  x     1 Sat Nov  7 10:00:00 2026 claude\n",
		"bad start":   "  12     1 Sat Nov 77 10:00:00 2026 claude\n",
		"other order": "  12     1 2026-11-07 10:00:00 a b c claude\n",
	} {
		_, err := parsePS([]byte(out), time.UTC)
		assert.Error(t, err, name)
	}
}

func TestAncestorIn(t *testing.T) {
	table, err := parsePS([]byte(psOutput), time.UTC)
	require.NoError(t, err)
	claudeStart := uint64(time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC).Unix())

	found, ok := ancestorIn(table, 702, "claude", 5)
	assert.True(t, ok)
	assert.Equal(t, Process{PID: 700, Start: claudeStart}, found, "two levels up, by its path")

	_, ok = ancestorIn(table, 702, "claude", 2)
	assert.False(t, ok, "beyond maxDepth")
	_, ok = ancestorIn(table, 999, "claude", 5)
	assert.False(t, ok, "a pid ps does not list")
	_, ok = ancestorIn(table, 701, "launchd", 5)
	assert.False(t, ok, "pid 1 ends the walk")

	byName, ok := ancestorIn(table, 800, "claude", 5)
	assert.True(t, ok)
	assert.Equal(t, 800, byName.PID, "a bare command name counts too")
}
