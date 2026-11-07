//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/tmux"
)

const (
	appearTimeout = time.Minute
	trustWait     = 5 * time.Second
)

// TestDiscoveredSession starts claude by hand in a tmux pane, with only the global-style hook (through --settings,
// so ~/.claude/settings.json stays untouched). No prompt goes to the model, so it costs no tokens.
func TestDiscoveredSession(t *testing.T) {
	h := newHarness(t)
	settings := h.writeDiscoverSettings()
	var seen []string

	// The app types into a discovered session through its pane; /exit ends it with SessionEnd.
	info := h.startByHand("by-hand-1", settings, &seen)
	assert.True(t, info.Discovered)
	assert.True(t, store.ValidClaudeID(info.ID), "keyed by Claude's session id")
	assert.True(t, info.InTmux)
	assert.Contains(t, info.Attach, " -S ")
	assert.Equal(t, filepath.Base(h.repo), info.Repo)
	meta, err := h.cfg.Store.LoadMeta(info.ID)
	require.NoError(t, err)
	assert.NotZero(t, meta.PID, "the hook should find its claude process")
	require.Equal(t, http.StatusOK, h.call(http.MethodPost, "/api/session/"+info.ID+"/message", map[string]string{"text": "/exit"}, nil))
	h.waitGone(info.ID)

	// Killed without SessionEnd: the process check removes it.
	info = h.startByHand("by-hand-2", settings, &seen)
	meta, err = h.cfg.Store.LoadMeta(info.ID)
	require.NoError(t, err)
	require.NotZero(t, meta.PID)
	require.NoError(t, syscall.Kill(meta.PID, syscall.SIGKILL))
	h.waitGone(info.ID)
	events, err := os.ReadFile(filepath.Join(h.cfg.Store.Root(), "archive", info.ID+".jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(events), state.EventProcessGone)
	assert.NotContains(t, string(events), "SessionEnd")
}

func (h *harness) writeDiscoverSettings() string {
	h.t.Helper()
	command := strings.Join([]string{h.bin, "hook", "--discover", "--state-dir", h.cfg.Store.Root()}, " ")
	entry := []map[string]any{{"matcher": "*", "hooks": []map[string]any{{"type": "command", "command": command, "timeout": 5}}}}
	hooks := map[string]any{}
	for _, event := range hook.Events {
		hooks[event] = entry
	}
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	require.NoError(h.t, err)
	path := filepath.Join(h.t.TempDir(), "discover-settings.json")
	require.NoError(h.t, os.WriteFile(path, data, 0o600))
	return path
}

// startByHand runs claude in a new tmux session the way a person would, and waits for the app to list it.
func (h *harness) startByHand(name, settings string, seen *[]string) session.Info {
	h.t.Helper()
	ctx := context.Background()
	require.NoError(h.t, h.tmux.Start(ctx, tmux.NewSession{
		Name: name, Dir: h.repo, Width: 160, Height: 45, Command: []string{"claude", "--settings", settings},
	}))
	deadline := time.Now().Add(appearTimeout)
	trustAt := time.Now().Add(trustWait)
	for time.Now().Before(deadline) {
		for _, info := range h.manager.List() {
			if info.Discovered && info.Dir == h.repo && !slices.Contains(*seen, info.ID) {
				*seen = append(*seen, info.ID)
				return info
			}
		}
		if time.Now().After(trustAt) {
			trustAt = deadline
			h.trustIfAsked(name)
		}
		time.Sleep(pollEvery)
	}
	screen, err := h.tmux.Capture(ctx, name)
	h.t.Fatalf("the session did not appear; screen (capture err %v):\n%s", err, screen)
	return session.Info{}
}

// trustIfAsked answers the trust screen, which comes before any hook can fire.
func (h *harness) trustIfAsked(name string) {
	h.t.Helper()
	ctx := context.Background()
	screen, err := h.tmux.Capture(ctx, name)
	require.NoError(h.t, err)
	if !answer.IsTrustPrompt(screen) {
		return
	}
	h.t.Log("trust screen seen; trusting the folder")
	steps, err := answer.Trust(state.Session{Kind: state.KindTrust}, true)
	require.NoError(h.t, err)
	for _, step := range steps {
		require.Empty(h.t, step.Text)
		require.NoError(h.t, h.tmux.SendKeys(ctx, name, step.Keys...))
	}
}

func (h *harness) waitGone(id string) {
	h.t.Helper()
	deadline := time.Now().Add(appearTimeout)
	for time.Now().Before(deadline) {
		if h.call(http.MethodGet, "/api/session/"+id, nil, nil) == http.StatusNotFound {
			assert.FileExists(h.t, filepath.Join(h.cfg.Store.Root(), "archive", id+".json"))
			exists, err := h.cfg.Store.MetaExists(id)
			require.NoError(h.t, err)
			assert.False(h.t, exists)
			return
		}
		time.Sleep(pollEvery)
	}
	h.t.Fatalf("session %s did not leave the list", id)
}
