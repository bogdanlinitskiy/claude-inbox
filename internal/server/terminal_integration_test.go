//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/tmux"
)

const tmuxName = "PROJ-1-contact"

type liveTerminal struct {
	conn   *websocket.Conn
	tmux   tmux.Client
	socket string
}

func startLiveTerminal(t *testing.T) *liveTerminal {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	socket := fmt.Sprintf("claude-inbox-term-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if err := exec.Command("tmux", "-L", socket, "kill-server").Run(); err != nil {
			t.Logf("stop test tmux server: %v", err)
		}
	})
	client := tmux.New(socket)
	require.NoError(t, client.Start(context.Background(), tmux.NewSession{
		Name: tmuxName, Dir: t.TempDir(), Width: 160, Height: 45, Command: []string{"bash", "--norc", "--noprofile"},
	}))
	// As in the app (session.Manager.Start): without the status bar the window gets every row of the terminal.
	require.NoError(t, client.SetOption(context.Background(), tmuxName, "status", "off"))

	sessions := newFakeSessions()
	info := sessions.infos[sessionID]
	info.TmuxName = tmuxName
	info.State = state.View{Kind: state.KindIdle}
	sessions.infos[sessionID] = info
	sessions.attach = client.AttachCommand(tmuxName)

	var srv *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	srv = New(Config{
		Port: port, Token: testToken, Sessions: sessions,
		Web: fstest.MapFS{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	header := http.Header{}
	header.Set("Origin", ts.URL)
	header.Set("Cookie", tokenCookie+"="+testToken)
	conn, _, err := websocket.Dial(context.Background(), "ws://"+u.Host+"/api/session/"+sessionID+"/terminal", &websocket.DialOptions{HTTPHeader: header})
	require.NoError(t, err)
	return &liveTerminal{conn: conn, tmux: client, socket: socket}
}

// readUntil collects terminal output until it contains want.
func (lt *liveTerminal) readUntil(t *testing.T, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		_, data, err := lt.conn.Read(ctx)
		require.NoError(t, err, "output so far:\n%s", out.String())
		out.Write(data)
	}
	return out.String()
}

func (lt *liveTerminal) windowSize(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", lt.socket, "display", "-p", "-t", "="+tmuxName+":", "#{window_width}x#{window_height}").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestTerminalTypesResizesAndDetaches(t *testing.T) {
	lt := startLiveTerminal(t)
	ctx := context.Background()

	require.NoError(t, lt.conn.Write(ctx, websocket.MessageBinary, []byte("echo marker-$((6*7))\r")))
	lt.readUntil(t, "marker-42")

	resize, err := json.Marshal(control{Type: controlResize, Cols: 100, Rows: 30})
	require.NoError(t, err)
	require.NoError(t, lt.conn.Write(ctx, websocket.MessageText, resize))
	var size string
	ok := assert.Eventually(t, func() bool {
		size = lt.windowSize(t)
		return size == "100x30"
	}, 5*time.Second, 100*time.Millisecond)
	require.True(t, ok, "tmux window is %s, want 100x30", size)

	require.NoError(t, lt.conn.Close(websocket.StatusNormalClosure, ""))
	require.Eventually(t, func() bool {
		out, err := exec.Command("tmux", "-L", lt.socket, "list-clients").Output()
		return err == nil && strings.TrimSpace(string(out)) == ""
	}, 5*time.Second, 100*time.Millisecond, "the tmux client should be gone")
	alive, err := lt.tmux.Has(ctx, tmuxName)
	require.NoError(t, err)
	assert.True(t, alive, "closing the page's terminal must not end the session")
}

func TestTerminalIgnoresBadControlMessages(t *testing.T) {
	lt := startLiveTerminal(t)
	ctx := context.Background()

	for _, msg := range []string{`not json`, `{"type":"resize","cols":0,"rows":10}`, `{"type":"resize","cols":5000,"rows":10}`, `{"type":"other"}`} {
		require.NoError(t, lt.conn.Write(ctx, websocket.MessageText, []byte(msg)))
	}
	require.NoError(t, lt.conn.Write(ctx, websocket.MessageBinary, []byte("echo still-$((1+1))\r")))

	lt.readUntil(t, "still-2")
}

