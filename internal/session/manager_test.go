package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/internal/settings"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/tmux"
)

const (
	textPrefix  = "text:"
	pastePrefix = "paste:"
	repoName    = "acme-contact"
)

type fakeTmux struct {
	mu       sync.Mutex
	sessions map[string]bool
	started  []tmux.NewSession
	options  map[string]string
	sent     []string
	screen   string
	// exitOnSlashExit makes the session disappear when /exit is submitted, like claude does.
	exitOnSlashExit bool
	lastText        string
	// targets records where keys went, to tell a session name from a pane id.
	targets []string
	// captures counts the screen reads, so a test can see the backoff.
	captures int
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{sessions: map[string]bool{}, options: map[string]string{}}
}

func (f *fakeTmux) Start(_ context.Context, opts tmux.NewSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[opts.Name] = true
	f.started = append(f.started, opts)
	return nil
}

func (f *fakeTmux) SetOption(_ context.Context, name, option, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.options[name+" "+option] = value
	return nil
}

func (f *fakeTmux) SendKeys(_ context.Context, name string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, keys...)
	f.targets = append(f.targets, name)
	if f.exitOnSlashExit && f.lastText == exitCommand && slices.Contains(keys, keyEnter) {
		delete(f.sessions, name)
	}
	return nil
}

func (f *fakeTmux) SendText(_ context.Context, name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, textPrefix+text)
	f.targets = append(f.targets, name)
	f.lastText = text
	return nil
}

func (f *fakeTmux) PasteText(_ context.Context, name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, pastePrefix+text)
	f.targets = append(f.targets, name)
	f.lastText = text
	return nil
}

func (f *fakeTmux) Capture(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures++
	return f.screen, nil
}

func (f *fakeTmux) Has(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[name], nil
}

func (f *fakeTmux) Kill(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, name)
	return nil
}

func (f *fakeTmux) AttachCommand(name string) []string {
	return []string{"tmux", "attach-session", "-t", name}
}

func (f *fakeTmux) takeCaptures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.captures
	f.captures = 0
	return n
}

func (f *fakeTmux) takeSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := f.sent
	f.sent = nil
	return sent
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	m      *Manager
	tmux   *fakeTmux
	clock  *clock
	store  store.Dir
	repo   string
	branch string
	cfg    Config
	// sockets are the socket paths discovered sessions were reached through; dead are the processes that exited.
	sockets []string
	dead    map[proc.Process]bool
	// worktrees are the AddWorktree calls as "repoDir dir branch"; worktreeErr fails them.
	worktrees   []string
	worktreeErr error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, repoName)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755))

	f := &fixture{tmux: newFakeTmux(), clock: &clock{now: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}, repo: repo, dead: map[proc.Process]bool{}}
	f.store = store.New(t.TempDir())
	f.cfg = Config{
		Store: f.store,
		Tmux:  f.tmux,
		TmuxAt: func(socketPath string) Tmux {
			f.sockets = append(f.sockets, socketPath)
			return f.tmux
		},
		Alive:       func(p proc.Process) bool { return !f.dead[p] },
		Roots:       []string{root},
		HookCommand: []string{"/home/user/.local/bin/claude-inbox", "hook", "--state-dir", f.store.Root()},
		SendCommand: []string{"/home/user/.local/bin/claude-inbox", "send", "--state-dir", f.store.Root()},
		Claude:      "claude",
		Transcripts: t.TempDir(),
		// Tickets as some teams chain them in a branch name, to show the pattern is the configured one.
		TicketPattern: regexp.MustCompile(`PROJ-\d+(?:-PROJ-\d+)*`),
		StripPrefix:   "acme-",
		CurrentBranch: func(context.Context, string) (string, error) {
			return f.branch, nil
		},
		AddWorktree: func(_ context.Context, repoDir, dir, branch string) error {
			f.worktrees = append(f.worktrees, repoDir+" "+dir+" "+branch)
			return f.worktreeErr
		},
		Now:                 f.clock.Now,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrustWait:           3 * time.Second,
		DeliveryTimeout:     3 * time.Second,
		PollInterval:        time.Second,
		PaneCheckInterval:   time.Second,
		ScreenCheckInterval: 2 * time.Second,
	}
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)
	f.m = m
	return f
}

func (f *fixture) start(t *testing.T, ticket, prompt string) Info {
	t.Helper()
	info, err := f.m.Start(context.Background(), StartRequest{Dir: f.repo, Ticket: ticket, Title: "Contact form rate limit", Prompt: prompt})
	require.NoError(t, err)
	return info
}

func (f *fixture) hook(t *testing.T, id string, fields map[string]any) {
	t.Helper()
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, f.store.Append(id, store.Record{TS: f.clock.Now(), Source: store.SourceHook, Payload: body}))
}

func (f *fixture) kind(t *testing.T, id string) state.Kind {
	t.Helper()
	f.m.pollEvents()
	info, err := f.m.Get(id)
	require.NoError(t, err)
	return info.State.Kind
}

func askQuestion(options ...string) map[string]any {
	opts := make([]map[string]any, len(options))
	for i, label := range options {
		opts[i] = map[string]any{"label": label, "description": ""}
	}
	return map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
		"questions": []map[string]any{{"question": "Which color?", "header": "Color", "options": opts, "multiSelect": false}},
	}}
}

func askBash(command string) map[string]any {
	return map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": command}}
}

func readScreen(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "screens", name))
	require.NoError(t, err)
	return string(data)
}

func TestNewWritesHookSettings(t *testing.T) {
	f := newFixture(t)

	data, err := os.ReadFile(f.store.HookSettingsPath())
	require.NoError(t, err)
	var written struct {
		Hooks map[string][]settings.Matcher `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &written))
	assert.Len(t, written.Hooks, len(hook.Events))
	want := "'/home/user/.local/bin/claude-inbox' 'hook' '--state-dir' '" + f.store.Root() + "'"
	for _, event := range hook.Events {
		require.Len(t, written.Hooks[event], 1, event)
		assert.Equal(t, want, written.Hooks[event][0].Hooks[0].Command, event)
	}
}

func TestStart(t *testing.T) {
	f := newFixture(t)
	prompt := "/spec PROJ-1600"

	info := f.start(t, "PROJ-1600", prompt)

	assert.Equal(t, "PROJ-1600-contact", info.TmuxName)
	assert.Equal(t, state.KindIdle, info.State.Kind)
	require.Len(t, f.tmux.started, 1)
	started := f.tmux.started[0]
	assert.Equal(t, f.repo, started.Dir)
	assert.Equal(t, map[string]string{hook.SessionEnv: info.ID}, started.Env)
	assert.Equal(t, []string{"claude", "--settings", f.store.HookSettingsPath(), "--permission-mode", "auto", prompt}, started.Command)
	assert.Equal(t, "off", f.tmux.options["PROJ-1600-contact status"])
	assert.Equal(t, "on", f.tmux.options["PROJ-1600-contact mouse"])

	metas, err := f.store.LoadMetas()
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, info.ID, metas[0].ID)
}

func TestStartRefusesOtherFolders(t *testing.T) {
	f := newFixture(t)

	for _, dir := range []string{filepath.Join(filepath.Dir(f.repo), "not-a-repo"), t.TempDir(), f.repo + "/../" + repoName + "/.git"} {
		_, err := f.m.Start(context.Background(), StartRequest{Dir: dir})
		require.ErrorIs(t, err, ErrInvalidRepo, dir)
	}
	assert.Empty(t, f.tmux.started)
}

// The prompt is an argument of the claude command, so its line breaks arrive as they are.
func TestStartKeepsMultilinePrompt(t *testing.T) {
	f := newFixture(t)
	prompt := "First line.\nSecond line."

	_, err := f.m.Start(context.Background(), StartRequest{Dir: f.repo, Prompt: prompt})

	require.NoError(t, err)
	require.Len(t, f.tmux.started, 1)
	assert.Equal(t, prompt, f.tmux.started[0].Command[len(f.tmux.started[0].Command)-1])
}

func TestStartTakesTicketFromBranch(t *testing.T) {
	f := newFixture(t)
	f.branch = "PROJ-392-PROJ-1128"

	info := f.start(t, "", "")

	assert.Equal(t, "PROJ-392-PROJ-1128", info.Ticket)
	assert.Equal(t, "PROJ-392-PROJ-1128-contact", info.TmuxName)
	assert.Equal(t, f.branch, info.Branch)
	assert.Equal(t, []string{"claude", "--settings", f.store.HookSettingsPath(), "--permission-mode", "auto"}, f.tmux.started[0].Command)
}

func TestStartWithoutTicket(t *testing.T) {
	f := newFixture(t)
	f.branch = "master"

	info := f.start(t, "", "")

	assert.Empty(t, info.Ticket)
	assert.Equal(t, "no-ticket-contact", info.TmuxName)
}

func TestStartPicksAFreeName(t *testing.T) {
	f := newFixture(t)
	f.tmux.sessions["PROJ-1-contact"] = true

	second := f.start(t, "PROJ-1", "")
	third := f.start(t, "PROJ-1", "")

	assert.Equal(t, "PROJ-1-contact-2", second.TmuxName)
	assert.Equal(t, "PROJ-1-contact-3", third.TmuxName)
}

func TestStartKeepsPromptStartingWithDash(t *testing.T) {
	f := newFixture(t)
	f.start(t, "PROJ-1", "-v please")
	cmd := f.tmux.started[0].Command
	assert.Equal(t, " -v please", cmd[len(cmd)-1])
}

func TestEventsChangeStateAndNotifySubscribers(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()

	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": "c1"})
	f.hook(t, info.ID, askQuestion("Red", "Green"))
	f.m.pollEvents()

	require.Len(t, changes, 1, "one change per poll")
	change := <-changes
	assert.True(t, change.Live)
	assert.Equal(t, state.KindQuestion, change.Session.State.Kind)
	assert.True(t, change.Session.State.NeedsYou)

	f.hook(t, info.ID, map[string]any{"hook_event_name": "Stop", "agent_id": "a1"})
	f.m.pollEvents()
	assert.Empty(t, changes, "internal agent events change nothing")
}

func TestRename(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()
	title := "Rate limit, second try"

	renamed, err := f.m.Rename(info.ID, "  "+title+"  ")

	require.NoError(t, err)
	assert.Equal(t, title, renamed.Title)
	assert.Equal(t, info.TmuxName, renamed.TmuxName)
	require.Len(t, changes, 1)
	assert.Equal(t, title, (<-changes).Session.Title)
	meta, err := f.store.LoadMeta(info.ID)
	require.NoError(t, err)
	assert.Equal(t, title, meta.Title)
	assert.Equal(t, info.TmuxName, meta.TmuxName)
	assert.Len(t, f.tmux.started, 1, "the running session is not touched")
}

func TestRenameToEmptyClearsTheTitle(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")

	renamed, err := f.m.Rename(info.ID, " ")

	require.NoError(t, err)
	assert.Empty(t, renamed.Title)
}

func TestRenameUnknownSession(t *testing.T) {
	f := newFixture(t)

	_, err := f.m.Rename("zzzz9999", "Anything")

	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPin(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()

	pinned, err := f.m.Pin(info.ID, true)

	require.NoError(t, err)
	assert.True(t, pinned.Pinned)
	require.Len(t, changes, 1)
	assert.True(t, (<-changes).Session.Pinned)
	meta, err := f.store.LoadMeta(info.ID)
	require.NoError(t, err)
	assert.True(t, meta.Pinned)
	assert.Equal(t, info.TmuxName, meta.TmuxName)

	unpinned, err := f.m.Pin(info.ID, false)

	require.NoError(t, err)
	assert.False(t, unpinned.Pinned)
	meta, err = f.store.LoadMeta(info.ID)
	require.NoError(t, err)
	assert.False(t, meta.Pinned)
}

func TestPinKeepsTheTitle(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	title := "Rate limit"
	_, err := f.m.Rename(info.ID, title)
	require.NoError(t, err)

	_, err = f.m.Pin(info.ID, true)

	require.NoError(t, err)
	meta, err := f.store.LoadMeta(info.ID)
	require.NoError(t, err)
	assert.Equal(t, title, meta.Title)
	assert.True(t, meta.Pinned)
}

func TestPinUnknownSession(t *testing.T) {
	f := newFixture(t)

	_, err := f.m.Pin("zzzz9999", true)

	assert.ErrorIs(t, err, ErrNotFound)
}

func TestAnswerOption(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "question-2-options.txt")
	f.hook(t, info.ID, askQuestion("Red", "Green", "Blue"))
	require.Equal(t, state.KindQuestion, f.kind(t, info.ID))
	current, err := f.m.Get(info.ID)
	require.NoError(t, err)
	option := 1

	_, err = f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: current.State.Seq - 1, Option: &option})
	require.ErrorIs(t, err, ErrStale)
	assert.Empty(t, f.tmux.takeSent())

	answered, err := f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: current.State.Seq, Option: &option})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, f.tmux.takeSent())
	assert.Equal(t, DeliveryPending, answered.Delivery)

	f.clock.Advance(time.Second)
	f.hook(t, info.ID, map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion"})
	assert.Equal(t, state.KindWorking, f.kind(t, info.ID))
	delivered, err := f.m.Get(info.ID)
	require.NoError(t, err)
	assert.Equal(t, DeliveryNone, delivered.Delivery)
}

func TestAnswerNeedsExactlyOneField(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	option, text := 0, "x"

	for _, a := range []AnswerRequest{{}, {Option: &option, Text: &text}} {
		_, err := f.m.Answer(context.Background(), info.ID, a)
		require.ErrorIs(t, err, ErrInvalidAnswer)
	}
}

func TestAnswerFreeText(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "question-2-options.txt")
	f.hook(t, info.ID, askQuestion("Apple", "Pear"))
	current := f.current(t, info.ID)
	text := "mango, actually"

	_, err := f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: current.State.Seq, Text: &text})

	require.NoError(t, err)
	assert.Equal(t, []string{"3", textPrefix + text, keyEnter}, f.tmux.takeSent())
}

func TestAllowAndDeny(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "permission-bash-manual.txt")
	yes, no := true, false

	f.hook(t, info.ID, askBash("date > deny-test.txt"))
	_, err := f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Allow: &yes})
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, f.tmux.takeSent())

	f.hook(t, info.ID, map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash"})
	f.hook(t, info.ID, askBash("rm -rf build"))
	_, err = f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Allow: &no})
	require.NoError(t, err)
	assert.Equal(t, []string{"4"}, f.tmux.takeSent(), "No is entry 4 on the captured menu")
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID), "deny fires no hook, the app records it")
}

func TestDenyWithInstructions(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "permission-bash-manual.txt")
	f.hook(t, info.ID, askBash("date > deny-test.txt"))
	instead := "Do not run it. Just reply with the word skipped."

	answered, err := f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Text: &instead})

	require.NoError(t, err)
	assert.Equal(t, []string{"4", keyClearInput, textPrefix + instead, keyEnter}, f.tmux.takeSent())
	assert.Equal(t, DeliveryPending, answered.Delivery)
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID))
}

func TestMessageDeliveryRetryThenFailure(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	ctx := context.Background()
	prompt := "Also put the word banana in your reply."

	_, err := f.m.Message(ctx, info.ID, prompt)
	require.NoError(t, err)
	assert.Equal(t, []string{keyClearInput, textPrefix + prompt, keyEnter}, f.tmux.takeSent())

	f.clock.Advance(2 * time.Second)
	f.m.checkPending(ctx)
	assert.Empty(t, f.tmux.takeSent(), "not yet timed out")

	f.clock.Advance(2 * time.Second)
	f.m.checkPending(ctx)
	assert.Equal(t, []string{keyEnter}, f.tmux.takeSent(), "one retry")

	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)
	assert.Empty(t, f.tmux.takeSent())
	assert.Equal(t, DeliveryFailed, f.current(t, info.ID).Delivery)
}

func TestMultilineMessageIsPasted(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	prompt := "First line.\nSecond line."

	_, err := f.m.Message(context.Background(), info.ID, prompt)

	require.NoError(t, err)
	assert.Equal(t, []string{keyClearInput, pastePrefix + prompt, keyEnter}, f.tmux.takeSent())
}

func TestMessageConfirmedAfterRetry(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	ctx := context.Background()
	_, err := f.m.Message(ctx, info.ID, "hello")
	require.NoError(t, err)

	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)
	f.hook(t, info.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "hello"})
	f.m.pollEvents()

	assert.Equal(t, DeliveryNone, f.current(t, info.ID).Delivery)
	assert.Equal(t, state.KindWorking, f.current(t, info.ID).State.Kind)
}

func TestOptionAnswersAreNeverRetried(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "question-2-options.txt")
	ctx := context.Background()
	f.hook(t, info.ID, askQuestion("Red", "Green"))
	option := 0
	_, err := f.m.Answer(ctx, info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Option: &option})
	require.NoError(t, err)
	f.tmux.takeSent()

	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)

	assert.Empty(t, f.tmux.takeSent(), "an extra Enter would pick the selected entry")
	assert.Equal(t, DeliveryFailed, f.current(t, info.ID).Delivery)
}

func TestInterrupt(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "UserPromptSubmit"})
	require.Equal(t, state.KindWorking, f.kind(t, info.ID))

	_, err := f.m.Interrupt(context.Background(), info.ID)

	require.NoError(t, err)
	assert.Equal(t, []string{keyInterrupt}, f.tmux.takeSent())
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID))
}

func TestTrustScreenDetected(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	ctx := context.Background()
	f.tmux.screen = readScreen(t, "trust-prompt.txt")

	f.clock.Advance(2 * time.Second)
	f.m.checkTrust(ctx)
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID), "too early to look")

	f.clock.Advance(2 * time.Second)
	f.m.checkTrust(ctx)
	require.Equal(t, state.KindTrust, f.kind(t, info.ID))

	yes := true
	_, err := f.m.Answer(ctx, info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Trust: &yes})
	require.NoError(t, err)
	assert.Equal(t, []string{"Down", keyEnter}, f.tmux.takeSent())

	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": "c1"})
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID))
	assert.Equal(t, DeliveryNone, f.current(t, info.ID).Delivery)
}

func TestNoTrustCheckOnceClaudeReported(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "trust-prompt.txt")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": "c1"})
	f.m.pollEvents()

	f.clock.Advance(5 * time.Second)
	f.m.checkTrust(context.Background())

	assert.Equal(t, state.KindIdle, f.kind(t, info.ID))
}

func TestPaneGone(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	delete(f.tmux.sessions, info.TmuxName)

	f.m.checkPanes(context.Background())

	f.requireArchived(t, info.ID)
}

// requireArchived checks that an ended session left the list: its conversation is in History from then on.
func (f *fixture) requireArchived(t *testing.T, id string) {
	t.Helper()
	f.m.pollEvents()
	_, err := f.m.Get(id)
	require.ErrorIs(t, err, ErrNotFound)
	assert.FileExists(t, filepath.Join(f.store.Root(), "archive", id+".json"))
}

func TestBranchChangeIsPublished(t *testing.T) {
	f := newFixture(t)
	f.branch = "master"
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()

	f.branch = "PROJ-1"
	f.m.checkPanes(context.Background())

	require.Len(t, changes, 1)
	assert.Equal(t, "PROJ-1", (<-changes).Session.Branch)
	assert.Equal(t, "PROJ-1", f.current(t, info.ID).Branch)
}

func TestCloseExitsAndArchives(t *testing.T) {
	f := newFixture(t)
	f.tmux.exitOnSlashExit = true
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()

	require.NoError(t, f.m.Close(context.Background(), info.ID))

	assert.Equal(t, []string{keyClearInput, textPrefix + exitCommand, keyEnter}, f.tmux.takeSent())
	assert.Empty(t, f.m.List())
	change := <-changes
	assert.True(t, change.Removed)
	assert.FileExists(t, filepath.Join(f.store.Root(), "archive", info.ID+".json"))
	_, err := f.m.Get(info.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCloseInterruptsAWorkingSession(t *testing.T) {
	f := newFixture(t)
	f.tmux.exitOnSlashExit = true
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "UserPromptSubmit"})
	require.Equal(t, state.KindWorking, f.kind(t, info.ID))

	require.NoError(t, f.m.Close(context.Background(), info.ID))

	assert.Equal(t, []string{keyInterrupt, keyClearInput, textPrefix + exitCommand, keyEnter}, f.tmux.takeSent())
}

func TestEndedSessionLeavesTheList(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	changes, stop := f.m.Subscribe()
	defer stop()
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionEnd"})

	f.requireArchived(t, info.ID)

	require.Len(t, changes, 1)
	assert.True(t, (<-changes).Removed)
	assert.Empty(t, f.tmux.takeSent())
}

func TestRestartArchivesEndedSessions(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionEnd"})

	restarted, err := New(context.Background(), f.cfg)

	require.NoError(t, err)
	assert.Empty(t, restarted.List())
	assert.FileExists(t, filepath.Join(f.store.Root(), "archive", info.ID+".json"))
}

func TestRestartReplaysWithoutLiveChanges(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": "c1"})
	f.hook(t, info.ID, askBash("go test ./..."))

	restarted, err := New(context.Background(), f.cfg)
	require.NoError(t, err)
	changes, stop := restarted.Subscribe()
	defer stop()

	got, err := restarted.Get(info.ID)
	require.NoError(t, err)
	assert.Equal(t, state.KindPermission, got.State.Kind)
	assert.Equal(t, info.TmuxName, got.TmuxName)
	restarted.pollEvents()
	assert.Empty(t, changes, "replayed states are not news")
}

func TestRepos(t *testing.T) {
	f := newFixture(t)

	repos, err := f.m.Repos()

	require.NoError(t, err)
	root := filepath.Dir(f.repo)
	assert.Equal(t, []Repo{
		{Name: filepath.Base(root), Dir: root, Workspace: true, Group: groupWorkspaces},
		{Name: repoName, Dir: f.repo, Group: groupOther},
	}, repos)
}

// A workspace can hold repositories of its own.
func TestReposListsWorkspacesFirstThenTheirRepositoriesToo(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	inner := filepath.Join(workspace, "inner-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(inner, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "reports"), 0o755))
	f.cfg.Workspaces = []string{workspace}
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)

	repos, err := m.Repos()

	require.NoError(t, err)
	root := filepath.Dir(f.repo)
	assert.Equal(t, []Repo{
		{Name: filepath.Base(workspace), Dir: workspace, Workspace: true, Group: groupWorkspaces},
		{Name: filepath.Base(root), Dir: root, Workspace: true, Group: groupWorkspaces},
		{Name: repoName, Dir: f.repo, Group: groupOther},
		{Name: "inner-repo", Dir: inner, Group: groupOther},
	}, repos)

	info, err := m.Start(context.Background(), StartRequest{Dir: inner})
	require.NoError(t, err, "a listed repository inside a workspace can host a session")
	assert.Equal(t, inner, info.Dir)
}

func TestReposListsRecentRepositoriesFirst(t *testing.T) {
	f := newFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.MkdirAll(filepath.Join(elsewhere, ".git"), 0o755))
	plain := t.TempDir()
	f.transcript(t, "11111111-1111-4111-8111-111111111111", f.repo)
	f.transcript(t, "22222222-2222-4222-8222-222222222222", elsewhere)
	f.transcript(t, "33333333-3333-4333-8333-333333333333", plain)
	f.transcript(t, "44444444-4444-4444-8444-444444444444", filepath.Join(plain, "gone"))
	f.transcript(t, "55555555-5555-4555-8555-555555555555", elsewhere)
	touch := func(id string, age time.Duration) {
		path := filepath.Join(f.cfg.Transcripts, "-project", id+".jsonl")
		at := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, at, at))
	}
	touch("11111111-1111-4111-8111-111111111111", 3*time.Hour)
	touch("22222222-2222-4222-8222-222222222222", time.Hour)
	touch("55555555-5555-4555-8555-555555555555", 2*time.Hour)

	repos, err := f.m.Repos()

	require.NoError(t, err)
	root := filepath.Dir(f.repo)
	assert.Equal(t, []Repo{
		{Name: "elsewhere", Dir: elsewhere, Group: groupRecent},
		{Name: repoName, Dir: f.repo, Group: groupRecent},
		{Name: filepath.Base(root), Dir: root, Workspace: true, Group: groupWorkspaces},
	}, repos, "latest first, each once, only git repositories, and a root's repository not again among the others")

	info, err := f.m.Start(context.Background(), StartRequest{Dir: elsewhere})
	require.NoError(t, err, "a recent repository outside the roots can host a session")
	assert.Equal(t, elsewhere, info.Dir)
	_, err = f.m.Start(context.Background(), StartRequest{Dir: plain})
	require.ErrorIs(t, err, ErrInvalidRepo, "a folder Claude ran in that is no repository is not offered")
}

func gitRepos(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name, ".git"), 0o755))
	}
}

func repoGroups(t *testing.T, config string) []RepoGroup {
	t.Helper()
	var groups []RepoGroup
	require.NoError(t, json.Unmarshal([]byte(config), &groups))
	return groups
}

func TestReposListsConfiguredGroupsInConfigOrder(t *testing.T) {
	f := newFixture(t)
	apps, libs := t.TempDir(), t.TempDir()
	gitRepos(t, apps, "alpha", "beta", "gamma", "delta")
	gitRepos(t, libs, "consts", "tools")
	require.NoError(t, os.MkdirAll(filepath.Join(apps, "notes"), 0o755))
	f.transcript(t, "11111111-1111-4111-8111-111111111111", f.repo)
	important, rest := "Important", "Rest"
	f.cfg.RepoGroups = repoGroups(t, `[
		{"title": "", "repos": [{"workspace": "`+apps+`"}]},
		{"title": "`+important+`", "repos": [{"some": "`+apps+`/{gamma, alpha}"}, {"repo": "`+libs+`/consts"}]},
		{"title": "", "repos": [{"repo": "`+apps+`/beta/"}]},
		{"title": "`+rest+`", "repos": [{"all": "`+apps+`"}, {"all": "`+libs+`"}]}
	]`)
	f.cfg.Workspaces = []string{apps, libs}
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)

	repos, err := m.Repos()

	require.NoError(t, err)
	root := filepath.Dir(f.repo)
	assert.Equal(t, []Repo{
		{Name: filepath.Base(apps), Dir: apps, Workspace: true},
		{Name: "gamma", Dir: filepath.Join(apps, "gamma"), Group: important},
		{Name: "alpha", Dir: filepath.Join(apps, "alpha"), Group: important},
		{Name: "consts", Dir: filepath.Join(libs, "consts"), Group: important},
		{Name: "beta", Dir: filepath.Join(apps, "beta")},
		{Name: "delta", Dir: filepath.Join(apps, "delta"), Group: rest},
		{Name: "tools", Dir: filepath.Join(libs, "tools"), Group: rest},
		{Name: filepath.Base(libs), Dir: libs, Workspace: true, Group: groupWorkspaces},
		{Name: filepath.Base(root), Dir: root, Workspace: true, Group: groupWorkspaces},
	}, repos, "config order, each folder once at its first place, no recent ones, unplaced workspaces last")

	_, err = m.Start(context.Background(), StartRequest{Dir: f.repo})
	require.ErrorIs(t, err, ErrInvalidRepo, "a recent repository outside the groups is not offered")
}

func TestReposRefusesAGroupRepositoryThatIsNoGitRepository(t *testing.T) {
	f := newFixture(t)
	missing := filepath.Join(t.TempDir(), "typo")
	f.cfg.RepoGroups = repoGroups(t, `[{"title": "", "repos": [{"repo": "`+missing+`"}]}]`)
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)

	_, err = m.Repos()

	require.ErrorContains(t, err, missing)
}

func TestReposRefusesAGroupWorkspaceThatIsNoFolder(t *testing.T) {
	f := newFixture(t)
	missing := filepath.Join(t.TempDir(), "typo")
	f.cfg.RepoGroups = repoGroups(t, `[{"title": "", "repos": [{"workspace": "`+missing+`"}]}]`)
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)

	_, err = m.Repos()

	require.ErrorContains(t, err, missing)
}

func TestRepoSourceRefusesBadEntries(t *testing.T) {
	for _, entry := range []string{
		`{}`,
		`{"repo": "/a", "all": "/b"}`,
		`{"repos": "/a"}`,
		`{"repo": ""}`,
		`{"some": "/a/b"}`,
		`{"some": "/a/{b, }"}`,
		`{"some": "/a/{b"}`,
		`{"workspace": ""}`,
		`"/a"`,
	} {
		var s RepoSource
		assert.Error(t, json.Unmarshal([]byte(entry), &s), entry)
	}
}

func TestStartWithoutTicketPattern(t *testing.T) {
	f := newFixture(t)
	f.cfg.TicketPattern = nil
	m, err := New(context.Background(), f.cfg)
	require.NoError(t, err)
	f.branch = "PROJ-1-login"

	info, err := m.Start(context.Background(), StartRequest{Dir: f.repo})

	require.NoError(t, err)
	assert.Empty(t, info.Ticket)
	assert.Equal(t, "no-ticket-contact", info.TmuxName)
}

func TestTmuxBase(t *testing.T) {
	tests := []struct {
		name, ticket, repo, prefix, want string
	}{
		{name: "ticket and stripped repo", ticket: "PROJ-1", repo: "acme-wallet", prefix: "acme-", want: "PROJ-1-wallet"},
		{name: "no ticket", repo: "acme-wallet", prefix: "acme-", want: "wallet"},
		{name: "no prefix", ticket: "PROJ-1", repo: "acme-wallet", want: "PROJ-1-acme-wallet"},
		{name: "prefix elsewhere in the name", repo: "my-acme-wallet", prefix: "acme-", want: "my-acme-wallet"},
		{name: "unsafe characters", ticket: "PROJ-1", repo: "web.app:2", want: "PROJ-1-web_app_2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TmuxBase(tt.ticket, tt.repo, tt.prefix))
		})
	}
}

func TestStartInWorkspace(t *testing.T) {
	f := newFixture(t)
	root := filepath.Dir(f.repo)

	info, err := f.m.Start(context.Background(), StartRequest{Dir: root, Ticket: "PROJ-2485"})

	require.NoError(t, err)
	assert.Equal(t, root, info.Dir)
	assert.Equal(t, "PROJ-2485-"+filepath.Base(root), info.TmuxName)
	require.Len(t, f.tmux.started, 1)
	assert.Equal(t, root, f.tmux.started[0].Dir)
}

func TestRunStopsWithContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.m.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func (f *fixture) current(t *testing.T, id string) Info {
	t.Helper()
	f.m.pollEvents()
	info, err := f.m.Get(id)
	require.NoError(t, err)
	return info
}

const (
	discoveredID = "62b5862e-495a-4bd4-b4c2-7ec0201ce108"
	paneID       = "%12"
	socketPath   = "/tmp/tmux-1000/default"
)

var claudeProcess = proc.Process{PID: 4242, Start: 99}

// discovered writes what the global hook writes for a session the app did not start.
func (f *fixture) discovered(t *testing.T, inTmux bool) store.Meta {
	t.Helper()
	meta := store.Meta{
		ID: discoveredID, Repo: repoName, Dir: f.repo, CreatedAt: f.clock.Now(), Discovered: true,
		PID: claudeProcess.PID, PIDStart: claudeProcess.Start,
	}
	if inTmux {
		meta.TmuxSocket, meta.TmuxPane = socketPath, paneID
	}
	require.NoError(t, f.store.CreateMeta(meta))
	f.hook(t, discoveredID, map[string]any{"hook_event_name": "SessionStart", "session_id": discoveredID})
	return meta
}

func TestDiscoveredSessionAppearsLive(t *testing.T) {
	f := newFixture(t)
	f.branch = "PROJ-1508"
	changes, stop := f.m.Subscribe()
	defer stop()
	f.discovered(t, false)
	f.hook(t, discoveredID, askBash("go test ./..."))

	f.m.discover(context.Background())

	require.Len(t, changes, 1)
	change := <-changes
	assert.True(t, change.Live, "a session that already waits when it appears should alert")
	got := change.Session
	assert.Equal(t, discoveredID, got.ID)
	assert.True(t, got.Discovered)
	assert.False(t, got.InTmux)
	assert.Empty(t, got.Attach)
	assert.Equal(t, f.branch, got.Ticket)
	assert.Equal(t, state.KindPermission, got.State.Kind)

	f.m.discover(context.Background())
	assert.Empty(t, changes, "a known session is not added twice")
}

func TestDiscoverSkipsAppSessionsBeingStarted(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.store.SaveMeta(store.Meta{ID: "abcd1234", TmuxName: "PROJ-1-contact"}))

	f.m.discover(context.Background())

	assert.Empty(t, f.m.List())
}

func TestDiscoveredSessionOutsideTmuxIsViewOnly(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, false)
	f.hook(t, discoveredID, askQuestion("Red", "Blue"))
	f.m.discover(context.Background())
	ctx := context.Background()
	option := 0

	_, err := f.m.Answer(ctx, discoveredID, AnswerRequest{Seq: f.current(t, discoveredID).State.Seq, Option: &option})
	require.ErrorIs(t, err, ErrNotInTmux)
	_, err = f.m.Message(ctx, discoveredID, "hello")
	require.ErrorIs(t, err, ErrNotInTmux)
	_, err = f.m.Interrupt(ctx, discoveredID)
	require.ErrorIs(t, err, ErrNotInTmux)
	_, err = f.m.Attach(discoveredID)
	require.ErrorIs(t, err, ErrNotInTmux)
	assert.Empty(t, f.tmux.takeSent())
}

func TestDiscoveredSessionInTmuxIsAnsweredThroughItsPane(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, true)
	f.tmux.screen = readScreen(t, "question-2-options.txt")
	f.hook(t, discoveredID, askQuestion("Red", "Blue"))
	f.m.discover(context.Background())
	option := 1

	_, err := f.m.Answer(context.Background(), discoveredID, AnswerRequest{Seq: f.current(t, discoveredID).State.Seq, Option: &option})

	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, f.tmux.takeSent())
	assert.Equal(t, []string{paneID}, f.tmux.targets)
	assert.Equal(t, []string{socketPath}, f.sockets)
	attach, err := f.m.Attach(discoveredID)
	require.NoError(t, err)
	assert.Equal(t, []string{"tmux", "attach-session", "-t", paneID}, attach)
	assert.True(t, f.current(t, discoveredID).InTmux)
}

func TestDiscoveredSessionLeavesOnSessionEnd(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, false)
	f.m.discover(context.Background())
	changes, stop := f.m.Subscribe()
	defer stop()

	f.hook(t, discoveredID, map[string]any{"hook_event_name": "SessionEnd"})
	f.m.pollEvents()

	require.Len(t, changes, 1)
	assert.True(t, (<-changes).Removed)
	assert.Empty(t, f.m.List())
	assert.FileExists(t, filepath.Join(f.store.Root(), "archive", discoveredID+".json"))
}

func TestDiscoveredSessionLeavesWhenClaudeIsGone(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, true)
	f.m.discover(context.Background())

	f.m.checkPanes(context.Background())
	f.m.pollEvents()
	require.Len(t, f.m.List(), 1, "a running claude keeps the session")

	f.dead[claudeProcess] = true
	f.m.checkPanes(context.Background())
	f.m.pollEvents()

	assert.Empty(t, f.m.List())
}

func TestDiscoveredSessionWithoutProcessStays(t *testing.T) {
	f := newFixture(t)
	meta := store.Meta{ID: discoveredID, Dir: f.repo, CreatedAt: f.clock.Now(), Discovered: true}
	require.NoError(t, f.store.CreateMeta(meta))
	f.m.discover(context.Background())

	f.m.checkPanes(context.Background())
	f.m.pollEvents()

	assert.Len(t, f.m.List(), 1)
}

func TestHideDiscoveredSession(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, true)
	f.m.discover(context.Background())

	require.NoError(t, f.m.Close(context.Background(), discoveredID))

	assert.Empty(t, f.tmux.takeSent(), "hiding sends no keys")
	assert.Empty(t, f.m.List())

	f.m.discover(context.Background())
	assert.Empty(t, f.m.List(), "a hidden session stays hidden until the hook reports it again")

	f.discovered(t, true)
	f.m.discover(context.Background())
	assert.Len(t, f.m.List(), 1)
}

func TestRestartArchivesEndedDiscoveredSessions(t *testing.T) {
	f := newFixture(t)
	f.discovered(t, false)
	f.hook(t, discoveredID, map[string]any{"hook_event_name": "SessionEnd"})

	restarted, err := New(context.Background(), f.cfg)

	require.NoError(t, err)
	assert.Empty(t, restarted.List())
	assert.FileExists(t, filepath.Join(f.store.Root(), "archive", discoveredID+".json"))
}

func TestAppSessionInfo(t *testing.T) {
	f := newFixture(t)

	info := f.start(t, "PROJ-1", "")

	assert.False(t, info.Discovered)
	assert.True(t, info.InTmux)
	assert.Equal(t, "tmux attach-session -t "+info.TmuxName, info.Attach)
}

// ended starts an app session whose Claude reported conversationID and then exited, taking its tmux session along.
const transcriptPrompt = "Fix the bonus rollback"

// transcript writes a conversation as Claude Code records it under ~/.claude/projects.
func (f *fixture) transcript(t *testing.T, claudeSessionID, dir string) {
	t.Helper()
	prompt, err := json.Marshal(map[string]any{"type": "user", "cwd": dir, "gitBranch": "PROJ-7-old", "message": map[string]any{"content": transcriptPrompt}})
	require.NoError(t, err)
	reply, err := json.Marshal(map[string]any{"type": "assistant", "cwd": dir, "message": map[string]any{"content": []map[string]any{{"type": "text", "text": "On it."}}}})
	require.NoError(t, err)
	path := filepath.Join(f.cfg.Transcripts, "-project", claudeSessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, slices.Concat(prompt, []byte("\n"), reply, []byte("\n")), 0o600))
}

func TestResumeConversation(t *testing.T) {
	f := newFixture(t)
	f.branch = "PROJ-1600-bonus"
	f.transcript(t, discoveredID, f.repo)

	info, err := f.m.ResumeConversation(context.Background(), discoveredID)

	require.NoError(t, err)
	assert.Equal(t, "PROJ-1600", info.Ticket, "the ticket of the branch the folder is on now")
	assert.Equal(t, transcriptPrompt, info.Title)
	assert.Equal(t, "PROJ-1600-contact", info.TmuxName)
	assert.False(t, info.Discovered)
	require.Len(t, f.tmux.started, 1)
	started := f.tmux.started[0]
	assert.Equal(t, f.repo, started.Dir)
	assert.Equal(t, map[string]string{hook.SessionEnv: info.ID}, started.Env)
	assert.Equal(t, []string{"claude", "--settings", f.store.HookSettingsPath(), "--permission-mode", "auto", resumeFlag, discoveredID}, started.Command)
	assert.Equal(t, "off", f.tmux.options[info.TmuxName+" status"])

	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": discoveredID})
	assert.Equal(t, state.KindIdle, f.kind(t, info.ID))
	assert.Equal(t, discoveredID, f.current(t, info.ID).ClaudeSessionID)
	list, err := f.m.Conversations()
	require.NoError(t, err)
	assert.Empty(t, list, "a resumed conversation leaves History")
}

func TestResumeConversationRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("no transcript", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.m.ResumeConversation(ctx, discoveredID)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Empty(t, f.tmux.started)
	})

	t.Run("runs elsewhere", func(t *testing.T) {
		f := newFixture(t)
		f.transcript(t, discoveredID, f.repo)
		f.discovered(t, false)
		f.m.discover(ctx)
		_, err := f.m.ResumeConversation(ctx, discoveredID)
		require.ErrorIs(t, err, ErrStillRunning)
		assert.Empty(t, f.tmux.started)
	})

	t.Run("folder gone", func(t *testing.T) {
		f := newFixture(t)
		f.transcript(t, discoveredID, filepath.Join(t.TempDir(), "gone"))
		_, err := f.m.ResumeConversation(ctx, discoveredID)
		require.ErrorIs(t, err, ErrNoFolder)
		assert.Empty(t, f.tmux.started)
	})
}

func TestConversationsLeaveOutListedSessions(t *testing.T) {
	f := newFixture(t)
	appConversation := "11111111-1111-4111-8111-111111111111"
	f.transcript(t, discoveredID, f.repo)
	f.transcript(t, appConversation, f.repo)
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": appConversation})
	f.m.pollEvents()

	list, err := f.m.Conversations()

	require.NoError(t, err)
	require.Len(t, list, 1, "the app session holds its conversation")
	assert.Equal(t, Conversation{
		ID: discoveredID, Title: transcriptPrompt, Ticket: "PROJ-7", Repo: repoName, Dir: f.repo, Branch: "PROJ-7-old",
		UpdatedAt: list[0].UpdatedAt,
	}, list[0])

	f.discovered(t, false)
	f.m.discover(context.Background())
	list, err = f.m.Conversations()
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestConversationsLeaveOutGoneFolders(t *testing.T) {
	f := newFixture(t)
	f.transcript(t, discoveredID, filepath.Join(t.TempDir(), "gone"))

	list, err := f.m.Conversations()

	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestPreview(t *testing.T) {
	f := newFixture(t)
	f.transcript(t, discoveredID, f.repo)

	p, err := f.m.Preview(discoveredID)

	require.NoError(t, err)
	assert.Equal(t, transcriptPrompt, p.FirstPrompt)
	_, err = f.m.Preview("11111111-1111-4111-8111-111111111111")
	require.ErrorIs(t, err, ErrNotFound)
}

// The case from 2026-11-06: Allow ran a command that took 40 s, so no hook came for 40 s. The menu left the screen,
// so the answer counts as delivered, and a second click sends nothing.
func TestAllowLandsWithoutAHook(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	ctx := context.Background()
	f.tmux.screen = readScreen(t, "permission-bash-manual.txt")
	f.hook(t, info.ID, askBash("sleep 40"))
	seq := f.current(t, info.ID).State.Seq
	yes := true

	_, err := f.m.Answer(ctx, info.ID, AnswerRequest{Seq: seq, Allow: &yes})
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, f.tmux.takeSent())

	_, err = f.m.Answer(ctx, info.ID, AnswerRequest{Seq: seq, Allow: &yes})
	require.ErrorIs(t, err, ErrAlreadySent)
	assert.Empty(t, f.tmux.takeSent(), "the second click must not type 1 into the prompt")

	f.tmux.screen = readScreen(t, "after-deny-manual.txt")
	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)

	got := f.current(t, info.ID)
	assert.Equal(t, DeliveryNone, got.Delivery)
	assert.Equal(t, state.KindWorking, got.State.Kind)
	assert.Empty(t, f.tmux.takeSent())
}

func TestAnswerSendsNothingWhenTheMenuIsGone(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "after-deny-manual.txt")
	f.hook(t, info.ID, askBash("date"))
	yes, option := true, 0

	_, err := f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Allow: &yes})
	require.ErrorIs(t, err, ErrNothingOnScreen)

	f.hook(t, info.ID, askQuestion("Red", "Green"))
	_, err = f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Option: &option})
	require.ErrorIs(t, err, ErrNothingOnScreen)

	trust := true
	_, err = f.m.Answer(context.Background(), info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Trust: &trust})
	require.ErrorIs(t, err, ErrNothingOnScreen)
	assert.Empty(t, f.tmux.takeSent())
}

func TestLateHookClearsNotSent(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	ctx := context.Background()
	f.tmux.screen = readScreen(t, "question-2-options.txt")
	f.hook(t, info.ID, askQuestion("Red", "Green"))
	option := 0
	_, err := f.m.Answer(ctx, info.ID, AnswerRequest{Seq: f.current(t, info.ID).State.Seq, Option: &option})
	require.NoError(t, err)
	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)
	require.Equal(t, DeliveryFailed, f.current(t, info.ID).Delivery, "the same menu is still on the screen")

	f.clock.Advance(30 * time.Second)
	f.hook(t, info.ID, map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion"})

	got := f.current(t, info.ID)
	assert.Equal(t, DeliveryNone, got.Delivery)
	assert.Equal(t, state.KindWorking, got.State.Kind)
}

const screenMenu = "● Done.\n\n New MCP server found in .mcp.json: jira\n\n ❯ 1. Use this MCP server\n   2. Continue without it\n\n Enter to confirm · Esc to cancel\n"

// done starts a session that has finished a turn and has been quiet since.
func (f *fixture) done(t *testing.T) Info {
	t.Helper()
	info := f.start(t, "PROJ-1", "")
	f.hook(t, info.ID, map[string]any{"hook_event_name": "SessionStart", "session_id": "c1"})
	f.hook(t, info.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "All tests pass."})
	require.Equal(t, state.KindDone, f.kind(t, info.ID))
	return info
}

func TestMenuWithoutHookIsReadFromTheScreen(t *testing.T) {
	f := newFixture(t)
	info := f.done(t)
	ctx := context.Background()
	f.tmux.screen = screenMenu

	f.clock.Advance(time.Second)
	f.m.checkScreens(ctx)
	assert.Equal(t, state.KindDone, f.kind(t, info.ID), "not quiet long enough yet")

	f.clock.Advance(2 * time.Second)
	f.m.checkScreens(ctx)
	got := f.current(t, info.ID)
	require.Equal(t, state.KindQuestion, got.State.Kind)
	assert.True(t, got.State.FromScreen)
	assert.True(t, got.State.NeedsYou)
	require.Len(t, got.State.Questions, 1)
	assert.Equal(t, "New MCP server found in .mcp.json: jira", got.State.Questions[0].Question)
	assert.Equal(t, "Continue without it", got.State.Questions[0].Options[1].Label)

	option := 1
	_, err := f.m.Answer(ctx, info.ID, AnswerRequest{Seq: got.State.Seq, Option: &option})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, f.tmux.takeSent())

	f.tmux.screen = readScreen(t, "after-deny-manual.txt")
	f.clock.Advance(4 * time.Second)
	f.m.checkPending(ctx)
	back := f.current(t, info.ID)
	assert.Equal(t, state.KindDone, back.State.Kind, "no hook follows such a menu; the state before it comes back")
	assert.Equal(t, "All tests pass.", back.State.LastMessage)
	assert.Equal(t, DeliveryNone, back.Delivery)
}

func TestMenuReadFromTheScreenGoesWhenAnsweredInTheTerminal(t *testing.T) {
	f := newFixture(t)
	info := f.done(t)
	ctx := context.Background()
	f.tmux.screen = screenMenu
	f.clock.Advance(3 * time.Second)
	f.m.checkScreens(ctx)
	require.Equal(t, state.KindQuestion, f.kind(t, info.ID))

	f.clock.Advance(3 * time.Second)
	f.m.checkScreens(ctx)
	asked := f.current(t, info.ID)
	assert.Equal(t, state.KindQuestion, asked.State.Kind, "the same menu is not asked twice")

	f.tmux.screen = readScreen(t, "after-deny-manual.txt")
	f.clock.Advance(3 * time.Second)
	f.m.checkScreens(ctx)
	assert.Equal(t, state.KindDone, f.kind(t, info.ID))
}

// A session nobody touches should not cost a capture-pane every couple of seconds for as long as it is open.
func TestScreenReadsBackOffWhileNothingIsOnTheScreen(t *testing.T) {
	f := newFixture(t)
	f.done(t)
	ctx := context.Background()
	f.tmux.screen = "● All tests pass."
	f.tmux.takeCaptures()

	// Ten minutes, offered every two seconds. Without the backoff that is 300 reads.
	for range 300 {
		f.clock.Advance(2 * time.Second)
		f.m.checkScreens(ctx)
	}
	backedOff := f.tmux.takeCaptures()
	assert.Less(t, backedOff, 30, "the wait should grow while the screen holds nothing")
	assert.Greater(t, backedOff, 0, "it should still read now and then")

	// A menu appears. The backoff is what it costs: the menu is seen within maxScreenInterval rather than at
	// once. From then on the session is read at the base interval again, so the menu leaving is seen quickly.
	f.tmux.screen = screenMenu
	for range maxScreenInterval / (2 * time.Second) {
		f.clock.Advance(2 * time.Second)
		f.m.checkScreens(ctx)
	}
	require.Equal(t, state.KindQuestion, f.kind(t, f.m.List()[0].ID), "seen within maxScreenInterval")

	for range 15 {
		f.clock.Advance(2 * time.Second)
		f.m.checkScreens(ctx)
	}
	assert.Greater(t, f.tmux.takeCaptures(), 10, "a session showing a menu is read at the base interval")
}

func TestScreenIsNotReadWhileAHookedQuestionWaits(t *testing.T) {
	f := newFixture(t)
	info := f.start(t, "PROJ-1", "")
	f.tmux.screen = readScreen(t, "permission-bash-manual.txt")
	f.hook(t, info.ID, askBash("date"))
	f.m.pollEvents()

	f.clock.Advance(10 * time.Second)
	f.m.checkScreens(context.Background())

	got := f.current(t, info.ID)
	assert.Equal(t, state.KindPermission, got.State.Kind)
	assert.False(t, got.State.FromScreen)
}

func TestStartInWorktree(t *testing.T) {
	f := newFixture(t)
	f.branch = "PROJ-1612-part-2"

	info, err := f.m.Start(context.Background(), StartRequest{Dir: f.repo, Worktree: true, Branch: " PROJ-1612-part-2 "})

	require.NoError(t, err)
	root := filepath.Dir(f.repo)
	wantDir := filepath.Join(root+worktreesSuffix, repoName, f.branch)
	assert.Equal(t, []string{f.repo + " " + wantDir + " " + f.branch}, f.worktrees)
	assert.Equal(t, wantDir, info.Dir)
	assert.True(t, info.Worktree)
	assert.Equal(t, repoName, info.Repo)
	assert.Equal(t, "PROJ-1612", info.Ticket, "the ticket comes from the new branch")
	require.Len(t, f.tmux.started, 1)
	assert.Equal(t, wantDir, f.tmux.started[0].Dir)
}

func TestStartInRepositoryFolderByDefault(t *testing.T) {
	f := newFixture(t)

	info := f.start(t, "PROJ-1", "")

	assert.Empty(t, f.worktrees)
	assert.False(t, info.Worktree)
	assert.Equal(t, f.repo, info.Dir)
}

func TestStartInWorktreeRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.m.Start(ctx, StartRequest{Dir: f.repo, Worktree: true, Branch: " "})
	require.ErrorIs(t, err, ErrNoBranch)

	_, err = f.m.Start(ctx, StartRequest{Dir: filepath.Dir(f.repo), Worktree: true, Branch: "x"})
	require.ErrorIs(t, err, ErrNoWorktree, "a workspace is no repository")

	f.worktreeErr = errors.New("fatal: 'x' is already checked out")
	_, err = f.m.Start(ctx, StartRequest{Dir: f.repo, Worktree: true, Branch: "x"})
	require.ErrorIs(t, err, ErrWorktree)

	assert.Empty(t, f.tmux.started)
}
