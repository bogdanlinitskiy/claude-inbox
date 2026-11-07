//go:build integration

// Package e2e runs the end-to-end checks against a real claude, through the HTTP API, with a private
// tmux server. It costs tokens, so it runs only with INBOX_E2E=1 (make test-e2e).
package e2e

import (
	"bytes"
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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/internal/server"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/tmux"
	"github.com/bogdanlinitskiy/claude-inbox/web"
)

const (
	enableEnv   = "INBOX_E2E"
	repoName    = "e2e"
	ticket      = "PROJ-E2E"
	turnTimeout = 3 * time.Minute
	pollEvery   = 500 * time.Millisecond
	modeKey     = "BTab"
)

type harness struct {
	t       *testing.T
	base    string
	origin  string
	token   string
	tmux    tmux.Client
	socket  string
	repo    string
	bin     string
	cfg     session.Config
	manager *session.Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv(enableEnv) != "1" {
		t.Skipf("set %s=1 to run the end-to-end test against a real claude (it costs tokens)", enableEnv)
	}
	for _, tool := range []string{"tmux", "claude", "git", "go"} {
		_, err := exec.LookPath(tool)
		require.NoError(t, err, "%s is needed", tool)
	}
	work := t.TempDir()
	bin := filepath.Join(work, "claude-inbox")
	build := exec.Command("go", "build", "-o", bin, "./cmd/claude-inbox")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build the hook binary: %s", out)

	// A fixed path keeps trusted-folder entries in ~/.claude.json to one; the first run meets the trust screen.
	root := filepath.Join(os.TempDir(), "claude-inbox-e2e-root")
	repo := filepath.Join(root, repoName)
	require.NoError(t, os.RemoveAll(repo))
	require.NoError(t, os.MkdirAll(repo, 0o755))
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch", "master"},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "--quiet", "--allow-empty", "-m", "base"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	socket := fmt.Sprintf("claude-inbox-e2e-%d", os.Getpid())
	t.Cleanup(func() {
		if err := exec.Command("tmux", "-L", socket, "kill-server").Run(); err != nil {
			t.Logf("stop test tmux server: %v", err)
		}
	})
	client := tmux.New(socket)
	stateDir := filepath.Join(work, "state")
	h := &harness{t: t, tmux: client, socket: socket, repo: repo, bin: bin}
	h.cfg = session.Config{
		Store:               store.New(stateDir),
		Tmux:                client,
		TmuxAt:              func(socketPath string) session.Tmux { return tmux.NewAt(socketPath) },
		Alive:               proc.New("/proc").Alive,
		Roots:               []string{root},
		HookCommand:         []string{bin, "hook", "--state-dir", stateDir},
		Claude:              "claude",
		CurrentBranch:       gitdiff.CurrentBranch,
		AddWorktree:         gitdiff.AddWorktree,
		Now:                 time.Now,
		Logger:              slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		TrustWait:           3 * time.Second,
		DeliveryTimeout:     10 * time.Second,
		KeyPause:            300 * time.Millisecond,
		PollInterval:        200 * time.Millisecond,
		PaneCheckInterval:   2 * time.Second,
		ScreenCheckInterval: 2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.manager, err = session.New(ctx, h.cfg)
	require.NoError(t, err)
	go h.manager.Run(ctx)

	var srv *server.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	h.token, err = server.NewToken()
	require.NoError(t, err)
	srv = server.New(server.Config{
		Port: port, Token: h.token, Sessions: h.manager, Diff: gitdiff.Compute,
		OpenIDE: func(context.Context, string) error { return nil },
		Web:     web.Files, Logger: h.cfg.Logger,
	})
	h.base, h.origin = ts.URL, ts.URL
	return h
}

// call sends a request the way the page does and decodes the JSON answer into out (when given).
func (h *harness) call(method, path string, body, out any) int {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(h.t, err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	require.NoError(h.t, err)
	req.Header.Set("Origin", h.origin)
	req.Header.Set("Cookie", "inbox_token="+h.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close() //nolint:errcheck // test response body
	data, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	if out != nil && len(data) > 0 && resp.StatusCode < 300 {
		require.NoError(h.t, json.Unmarshal(data, out), string(data))
	}
	if resp.StatusCode >= 300 {
		h.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

func (h *harness) get(id string) session.Info {
	h.t.Helper()
	var info session.Info
	require.Equal(h.t, http.StatusOK, h.call(http.MethodGet, "/api/session/"+id, nil, &info))
	return info
}

// waitFor polls the session until cond holds; on timeout it prints the session's screen to show why.
func (h *harness) waitFor(id, what string, cond func(session.Info) bool) session.Info {
	h.t.Helper()
	deadline := time.Now().Add(turnTimeout)
	var info session.Info
	for time.Now().Before(deadline) {
		info = h.get(id)
		if cond(info) {
			return info
		}
		time.Sleep(pollEvery)
	}
	screen, err := h.tmux.Capture(context.Background(), info.TmuxName)
	h.t.Fatalf("timed out waiting for %s; state %+v\nscreen (capture err %v):\n%s", what, info.State, err, screen)
	return info
}

func kindIs(kinds ...state.Kind) func(session.Info) bool {
	return func(info session.Info) bool {
		for _, k := range kinds {
			if info.State.Kind == k {
				return true
			}
		}
		return false
	}
}

// message sends a prompt and waits until Claude has taken it (UserPromptSubmit), so a following wait does not
// see the state from before.
func (h *harness) message(id, prompt string) {
	h.t.Helper()
	require.Equal(h.t, http.StatusOK, h.call(http.MethodPost, "/api/session/"+id+"/message", map[string]string{"text": prompt}, nil))
	h.waitFor(id, "the prompt to be taken", func(info session.Info) bool {
		return info.Delivery != session.DeliveryPending && info.State.Kind != state.KindIdle
	})
}

func (h *harness) answer(id string, a map[string]any) {
	h.t.Helper()
	a["seq"] = h.get(id).State.Seq
	require.Equal(h.t, http.StatusOK, h.call(http.MethodPost, "/api/session/"+id+"/answer", a, nil))
}

func lower(s string) string {
	return strings.ToLower(s)
}

func TestEndToEnd(t *testing.T) {
	h := newHarness(t)

	// Start through the API, with a first prompt.
	var info session.Info
	require.Equal(t, http.StatusCreated, h.call(http.MethodPost, "/api/sessions", map[string]string{
		"dir": h.repo, "ticket": ticket, "title": "End-to-end", "prompt": "Reply with just the word ready.",
	}, &info))
	id := info.ID
	assert.Equal(t, ticket+"-e2e", info.TmuxName)

	// A folder Claude has not seen yet asks for trust before any hook fires.
	info = h.waitFor(id, "the first turn or the trust screen", kindIs(state.KindTrust, state.KindDone))
	if info.State.Kind == state.KindTrust {
		t.Log("trust screen seen; trusting the folder")
		h.answer(id, map[string]any{"trust": true})
		info = h.waitFor(id, "the first turn to finish", kindIs(state.KindDone))
	}
	assert.Contains(t, lower(info.State.LastMessage), "ready")

	// The live terminal shows the session.
	h.checkTerminal(id)

	// Question answered with an option.
	h.message(id, "Use the AskUserQuestion tool to ask me which color I prefer, with exactly the options red, green and blue. After I answer, reply with only the color.")
	info = h.waitFor(id, "the color question", kindIs(state.KindQuestion))
	require.True(t, info.State.CanAnswer)
	labels := []string{}
	for _, o := range info.State.Questions[0].Options {
		labels = append(labels, lower(o.Label))
	}
	green := indexOf(labels, "green")
	require.GreaterOrEqual(t, green, 0, "options: %v", labels)
	h.answer(id, map[string]any{"option": green})
	info = h.waitFor(id, "the color answer", kindIs(state.KindDone))
	assert.Contains(t, lower(info.State.LastMessage), "green")

	// Question answered with free text.
	h.message(id, "Use the AskUserQuestion tool to ask me which fruit I prefer, with exactly the options apple and pear. After I answer, reply with only my answer.")
	h.waitFor(id, "the fruit question", kindIs(state.KindQuestion))
	h.answer(id, map[string]any{"text": "mango"})
	info = h.waitFor(id, "the fruit answer", kindIs(state.KindDone))
	assert.Contains(t, lower(info.State.LastMessage), "mango")

	// Manual mode, so Bash asks for permission: Allow runs the command, Deny does not.
	require.NoError(t, h.tmux.SendKeys(context.Background(), info.TmuxName, modeKey))
	allowed, denied := filepath.Join(h.repo, "e2e-allowed.txt"), filepath.Join(h.repo, "e2e-denied.txt")
	h.message(id, "Run exactly this bash command and nothing else: date > e2e-allowed.txt")
	info = h.waitFor(id, "the first permission", kindIs(state.KindPermission))
	require.NotNil(t, info.State.Permission)
	assert.Contains(t, info.State.Permission.Detail, "e2e-allowed.txt")
	h.answer(id, map[string]any{"allow": true})
	h.waitFor(id, "the allowed command to finish", kindIs(state.KindDone))
	assert.FileExists(t, allowed)

	h.message(id, "Run exactly this bash command and nothing else: date > e2e-denied.txt")
	h.waitFor(id, "the second permission", kindIs(state.KindPermission))
	h.answer(id, map[string]any{"allow": false})
	h.waitFor(id, "idle after deny", kindIs(state.KindIdle))
	assert.NoFileExists(t, denied)

	// Interrupt a long turn.
	h.message(id, "Write a 600-word story about a lighthouse keeper. No tools.")
	h.waitFor(id, "the story to start", kindIs(state.KindWorking))
	require.Equal(t, http.StatusOK, h.call(http.MethodPost, "/api/session/"+id+"/interrupt", nil, nil))
	before := h.waitFor(id, "idle after interrupt", kindIs(state.KindIdle))

	// A restarted app rebuilds the same state from the events.
	restarted, err := session.New(context.Background(), h.cfg)
	require.NoError(t, err)
	again, err := restarted.Get(id)
	require.NoError(t, err)
	assert.Equal(t, before.State.Kind, again.State.Kind)
	assert.Equal(t, before.TmuxName, again.TmuxName)

	// Close ends the session and archives it.
	require.Equal(t, http.StatusNoContent, h.call(http.MethodDelete, "/api/session/"+id, nil, nil))
	alive, err := h.tmux.Has(context.Background(), info.TmuxName)
	require.NoError(t, err)
	assert.False(t, alive)
	assert.Equal(t, http.StatusNotFound, h.call(http.MethodGet, "/api/session/"+id, nil, nil))
}

func (h *harness) checkTerminal(id string) {
	h.t.Helper()
	u, err := url.Parse(h.base)
	require.NoError(h.t, err)
	header := http.Header{}
	header.Set("Origin", h.origin)
	header.Set("Cookie", "inbox_token="+h.token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+u.Host+"/api/session/"+id+"/terminal?cols=120&rows=40", &websocket.DialOptions{HTTPHeader: header})
	require.NoError(h.t, err)
	defer conn.CloseNow() //nolint:errcheck // test socket
	_, data, err := conn.Read(ctx)
	require.NoError(h.t, err)
	assert.NotEmpty(h.t, data, "the terminal should send the screen")
	require.NoError(h.t, conn.Close(websocket.StatusNormalClosure, ""))
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}
