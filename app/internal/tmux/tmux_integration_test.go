//go:build integration

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const screenTimeout = 5 * time.Second

// newTestClient runs a private tmux server for the test and stops it afterwards.
func newTestClient(t *testing.T) Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	socket := fmt.Sprintf("claude-inbox-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if err := exec.Command("tmux", "-L", socket, "kill-server").Run(); err != nil {
			t.Logf("stop test tmux server: %v", err)
		}
	})
	return New(socket)
}

func startBash(t *testing.T, c Client, name string, env map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background(), NewSession{
		Name: name, Dir: dir, Env: env, Width: 120, Height: 40,
		Command: []string{"bash", "--norc", "--noprofile"},
	}))
	return dir
}

func waitForScreen(t *testing.T, c Client, name, want string) string {
	t.Helper()
	deadline := time.Now().Add(screenTimeout)
	var screen string
	for time.Now().Before(deadline) {
		var err error
		screen, err = c.Capture(context.Background(), name)
		require.NoError(t, err)
		if strings.Contains(screen, want) {
			return screen
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Failf(t, "text did not appear on screen", "want %q, screen:\n%s", want, screen)
	return screen
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	name := "PROJ-1-wallet"
	sessionID := "abcd1234"

	dir := startBash(t, c, name, map[string]string{"INBOX_SESSION": sessionID})
	require.NoError(t, c.SetOption(ctx, name, "status", "off"))

	has, err := c.Has(ctx, name)
	require.NoError(t, err)
	assert.True(t, has)

	require.NoError(t, c.SendText(ctx, name, "echo marker-$INBOX_SESSION-$PWD"))
	require.NoError(t, c.SendKeys(ctx, name, "Enter"))
	waitForScreen(t, c, name, "marker-"+sessionID+"-"+dir)

	require.NoError(t, c.Kill(ctx, name))
	has, err = c.Has(ctx, name)
	require.NoError(t, err)
	assert.False(t, has)
}

func TestTargetsMatchNamesExactly(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	startBash(t, c, "PROJ-1-wallet-2", nil)

	has, err := c.Has(ctx, "PROJ-1-wallet")
	require.NoError(t, err)
	assert.False(t, has, "a prefix must not match another session")

	require.Error(t, c.SendText(ctx, "PROJ-1-wallet", "should not arrive"))
}

func TestSendTextIsLiteral(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	name := "literal"
	startBash(t, c, name, nil)

	cases := []string{
		`echo 'Enter C-c Escape'`,
		`echo 'semi;colon'`,
		`echo 'ends with a semicolon;'`,
		`echo "quotes 'and' \"more\" and \$HOME"`,
		`echo 'unicode: Привіт ✓'`,
	}
	for i, text := range cases {
		marker := fmt.Sprintf("case-%d", i)
		require.NoError(t, c.SendText(ctx, name, text+" "+marker))
		require.NoError(t, c.SendKeys(ctx, name, "Enter"))
		screen := waitForScreen(t, c, name, marker)
		assert.Contains(t, screen, text+" "+marker, "the command line must arrive unchanged")
	}
}

// bash asks for bracketed paste, so the pasted lines wait for Enter instead of running one by one.
func TestPasteTextKeepsLineBreaksAsText(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	name := "paste"
	startBash(t, c, name, nil)

	require.NoError(t, c.PasteText(ctx, name, "echo first-$((40+2))\necho second-$((40+2))"))
	waitForScreen(t, c, name, "echo second-$((40+2))")
	screen, err := c.Capture(ctx, name)
	require.NoError(t, err)
	assert.NotContains(t, screen, "first-42", "a line break must not run the line before Enter")

	require.NoError(t, c.SendKeys(ctx, name, "Enter"))
	waitForScreen(t, c, name, "second-42")
}

func TestHasWithoutServer(t *testing.T) {
	c := newTestClient(t)

	has, err := c.Has(context.Background(), "anything")

	require.NoError(t, err)
	assert.False(t, has)
}

// A session someone else started is reached through the socket path and pane id its claude saw in TMUX and
// TMUX_PANE.
func TestSocketPathAndPaneID(t *testing.T) {
	ctx := context.Background()
	socketDir, err := os.MkdirTemp("", "cit")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	socketPath := filepath.Join(socketDir, "s")
	c := NewAt(socketPath)
	t.Cleanup(func() {
		if err := exec.Command("tmux", "-S", socketPath, "kill-server").Run(); err != nil {
			t.Logf("stop test tmux server: %v", err)
		}
	})
	name := "by-hand"
	startBash(t, c, name, nil)
	out, err := exec.Command("tmux", "-S", socketPath, "display-message", "-p", "-t", "="+name+":", "#{pane_id}").Output()
	require.NoError(t, err)
	pane := strings.TrimSpace(string(out))
	require.Regexp(t, `^%[0-9]+$`, pane)

	require.NoError(t, c.SendText(ctx, pane, `echo "marker-${TMUX%%,*}-$TMUX_PANE"`))
	require.NoError(t, c.SendKeys(ctx, pane, "Enter"))
	waitForScreen(t, c, pane, "marker-"+socketPath+"-"+pane)

	has, err := c.Has(ctx, pane)
	require.NoError(t, err)
	assert.True(t, has)
	assert.Equal(t, []string{"tmux", "-S", socketPath, "attach-session", "-t", pane}, c.AttachCommand(pane))
}
