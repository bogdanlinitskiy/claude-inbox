// Package hook records one Claude Code hook payload: for a session the app started (Run), or for any other session
// on this machine through the global hook (Discover).
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/store"
)

const (
	// SessionEnv names the environment variable through which the app tells a Claude session its id.
	SessionEnv = "INBOX_SESSION"
	tmuxEnv    = "TMUX"
	paneEnv    = "TMUX_PANE"
)

// maxPayload bounds what one hook run reads; a larger payload is cut, stops being valid JSON and is kept as raw
// text, so the session's state stays where it was instead of the hook running out of memory.
const maxPayload = 8 << 20

var panePattern = regexp.MustCompile(`^%[0-9]+$`)

// Events are the hook events the app registers, for its own sessions and in the global settings; they are the ones
// the state machine reads (docs/architecture.md, "Hook modes").
var Events = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
	"PermissionRequest", "PermissionDenied", "Stop", "StopFailure",
}

// Run appends the payload on stdin to the event file of the session named in INBOX_SESSION. Without that
// variable the session was not started by the app and nothing is recorded. Run never writes to stdout, because
// Claude Code reads a hook's stdout as instructions.
func Run(dir store.Dir, getenv func(string) string, stdin io.Reader, now time.Time) error {
	id := getenv(SessionEnv)
	if id == "" {
		return nil
	}
	body, err := readPayload(stdin)
	if err != nil {
		return err
	}
	return appendPayload(dir, id, body, now)
}

// Discover records the payload of a session the app did not start, under Claude Code's session id. A session
// with INBOX_SESSION is the app's own: its Run hook records it, so Discover leaves it alone. On a session's first
// event Discover also writes its metadata, with the claude process that findClaude returns and the tmux pane the
// session runs in.
func Discover(dir store.Dir, getenv func(string) string, stdin io.Reader, now time.Time, findClaude func() (proc.Process, bool)) error {
	if getenv(SessionEnv) != "" {
		return nil
	}
	body, err := readPayload(stdin)
	if err != nil {
		return err
	}
	var p struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(body, &p); err != nil || !store.ValidClaudeID(p.SessionID) {
		return fmt.Errorf("%w: %q", store.ErrInvalidID, p.SessionID)
	}
	exists, err := dir.MetaExists(p.SessionID)
	if err != nil {
		return err
	}
	if !exists {
		if err := dir.CreateMeta(discoveredMeta(p.SessionID, p.Cwd, getenv, now, findClaude)); err != nil {
			return err
		}
	}
	return appendPayload(dir, p.SessionID, body, now)
}

func discoveredMeta(id, cwd string, getenv func(string) string, now time.Time, findClaude func() (proc.Process, bool)) store.Meta {
	m := store.Meta{ID: id, CreatedAt: now, Discovered: true}
	if filepath.IsAbs(cwd) {
		m.Dir = filepath.Clean(cwd)
		m.Repo = filepath.Base(m.Dir)
	}
	if claude, ok := findClaude(); ok {
		m.PID = claude.PID
		m.PIDStart = claude.Start
	}
	// TMUX is "<socket path>,<server pid>,<session index>".
	socket, _, _ := strings.Cut(getenv(tmuxEnv), ",")
	if pane := getenv(paneEnv); filepath.IsAbs(socket) && panePattern.MatchString(pane) {
		m.TmuxSocket = socket
		m.TmuxPane = pane
	}
	return m
}

func readPayload(stdin io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return nil, fmt.Errorf("read hook payload: %w", err)
	}
	return body, nil
}

func appendPayload(dir store.Dir, id string, body []byte, now time.Time) error {
	rec := store.Record{TS: now, Source: store.SourceHook}
	if json.Valid(body) {
		rec.Payload = body
	} else {
		rec.Raw = string(body)
	}
	if err := dir.Append(id, rec); err != nil {
		return fmt.Errorf("record hook payload: %w", err)
	}
	return nil
}
