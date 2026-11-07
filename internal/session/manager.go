// Package session runs the Claude sessions the app started: it starts and closes them in tmux, follows their
// event files, answers them, and tells subscribers whenever a session changes. It also follows the sessions the
// global hook discovered, and answers those that run in tmux.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/tmux"
	"github.com/bogdanlinitskiy/claude-inbox/internal/transcript"
)

const (
	terminalWidth      = 200
	terminalHeight     = 50
	permissionModeAuto = "auto"
	noTicket           = "no-ticket"
	subscriberBuffer   = 64
	closeWait          = 5 * time.Second
	closePoll          = 200 * time.Millisecond
	keyInterrupt       = "Escape"
	keyClearInput      = "C-c"
	keyEnter           = "Enter"
	exitCommand        = "/exit"
	resumeFlag         = "--resume"
	worktreesSuffix    = "-worktrees"
)

var (
	ErrNotFound     = errors.New("session not found")
	ErrInvalidRepo  = errors.New("not a listed repository or workspace")
	ErrNotInTmux    = errors.New("the session does not run in tmux")
	ErrStillRunning = errors.New("the conversation already runs in another session")
	ErrNoFolder     = errors.New("the conversation's folder no longer exists")
	ErrNoBranch     = errors.New("a worktree needs a branch name")
	ErrNoWorktree   = errors.New("a workspace is no repository, so it has no worktrees")
	// ErrWorktree: git could not add the worktree, e.g. the branch is checked out elsewhere; git says why.
	ErrWorktree = errors.New("could not add the worktree")

	unsafeNameChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
)

// Tmux is what the manager needs from tmux.Client.
type Tmux interface {
	Start(ctx context.Context, opts tmux.NewSession) error
	SetOption(ctx context.Context, name, option, value string) error
	SendKeys(ctx context.Context, name string, keys ...string) error
	SendText(ctx context.Context, name, text string) error
	PasteText(ctx context.Context, name, text string) error
	Capture(ctx context.Context, name string) (string, error)
	Has(ctx context.Context, name string) (bool, error)
	Kill(ctx context.Context, name string) error
	AttachCommand(name string) []string
}

type Config struct {
	Store store.Dir
	Tmux  Tmux
	// TmuxAt reaches the tmux server at a socket path, where a discovered session may run.
	TmuxAt func(socketPath string) Tmux
	// Alive reports whether a discovered session's claude process still runs.
	Alive func(p proc.Process) bool
	// Roots are the folders whose git repositories can host sessions.
	Roots []string
	// Workspaces are further folders a session can start in, listed before the roots; their repositories are not
	// listed.
	Workspaces []string
	// RepoGroups, when set, replace the start form's list of recent and other repositories; see Repos.
	RepoGroups []RepoGroup
	// HookCommand is the hook as Claude Code should run it, e.g. claude-inbox hook --state-dir <dir>.
	HookCommand []string
	Claude      string
	// Transcripts is Claude Code's folder of conversation transcripts, ~/.claude/projects. The folders Claude ran
	// in there are offered for new sessions too. Empty: none.
	Transcripts string
	// TicketPattern finds a session's ticket id in its git branch name; nil finds none.
	TicketPattern *regexp.Regexp
	// StripPrefix is left out of repository names in tmux session names, e.g. "acme-" for acme-wallet.
	StripPrefix   string
	CurrentBranch func(ctx context.Context, dir string) (string, error)
	// AddWorktree checks branch out into a new worktree of repoDir at dir (gitdiff.AddWorktree).
	AddWorktree func(ctx context.Context, repoDir, dir, branch string) error
	Now         func() time.Time
	Logger      *slog.Logger
	// TrustWait is how long after start a session may stay silent before the app looks for the trust screen.
	TrustWait time.Duration
	// DeliveryTimeout is how long a sent answer may go without any hook before it counts as not delivered.
	DeliveryTimeout time.Duration
	// ScreenCheckInterval is how long a session stays quiet before the app reads its screen for a menu no hook
	// reported, and how often it reads it again.
	ScreenCheckInterval time.Duration
	// KeyPause separates typed text from the Enter that submits it.
	KeyPause          time.Duration
	PollInterval      time.Duration
	PaneCheckInterval time.Duration
}

type Delivery string

const (
	DeliveryNone    Delivery = ""
	DeliveryPending Delivery = "pending"
	DeliveryFailed  Delivery = "failed"
)

// Info is a session as the page shows it.
type Info struct {
	ID        string     `json:"id"`
	Ticket    string     `json:"ticket"`
	Title     string     `json:"title"`
	Repo      string     `json:"repo"`
	Dir       string     `json:"dir"`
	TmuxName  string     `json:"tmuxName"`
	Branch    string     `json:"branch"`
	CreatedAt string     `json:"createdAt"`
	State     state.View `json:"state"`
	Delivery  Delivery   `json:"delivery,omitempty"`
	// Discovered: the global hook reported the session; the app did not start it.
	Discovered bool `json:"discovered"`
	// Worktree: the session runs in a git worktree the app made for it.
	Worktree bool `json:"worktree,omitempty"`
	// InTmux: the app can type into the session and show its terminal.
	InTmux bool `json:"inTmux"`
	// Attach is the command that attaches a terminal to the session's tmux session.
	Attach string `json:"attach,omitempty"`
	// ClaudeSessionID is Claude Code's id of the conversation, once its first hook has reported it.
	ClaudeSessionID string `json:"claudeSessionId,omitempty"`
}

// Conversation is a past conversation as History shows it.
type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Ticket    string `json:"ticket"`
	Repo      string `json:"repo"`
	Dir       string `json:"dir"`
	Branch    string `json:"branch"`
	UpdatedAt string `json:"updatedAt"`
}

// Change tells a subscriber that a session changed or was removed. Live is false for changes found while
// replaying old events, so they never raise alerts.
type Change struct {
	Session Info `json:"session"`
	Removed bool `json:"removed,omitempty"`
	Live    bool `json:"live"`
}

type StartRequest struct {
	Dir    string `json:"dir"`
	Ticket string `json:"ticket"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// Worktree starts the session in a new git worktree of the repository at Dir, on Branch.
	Worktree bool   `json:"worktree"`
	Branch   string `json:"branch"`
}

type entry struct {
	meta store.Meta
	// pane is set when inTmux is true.
	pane         pane
	inTmux       bool
	state        state.Session
	offset       int64
	branch       string
	pending      *pending
	delivery     Delivery
	trustChecked bool
	// lastEventAt is when the last event arrived; screenCheckedAt is when checkScreens last read the screen.
	lastEventAt     time.Time
	screenCheckedAt time.Time
	// send keeps two answers to one session from interleaving their keys.
	send sync.Mutex
}

type pending struct {
	sentAt time.Time
	// retryEnter: the send ended with text and Enter, so one more Enter is safe to try.
	retryEnter bool
	retried    bool
	// An answer waits for menu, or for the trust screen, to leave the screen; seq is the state it answers.
	menu  *answer.Menu
	trust bool
	seq   int
}

func (p *pending) isAnswer() bool {
	return p.menu != nil || p.trust
}

// stillOnScreen reports whether what the answer was for still shows, unchanged.
func (p *pending) stillOnScreen(screen string) bool {
	if p.trust {
		return answer.IsTrustPrompt(screen)
	}
	menu, ok := answer.ParseMenu(screen)
	return ok && menu.Same(*p.menu)
}

// pane is how keys reach a session: the tmux server it runs on and its target there.
type pane struct {
	tmux   Tmux
	target string
}

type Manager struct {
	cfg     Config
	mu      sync.Mutex
	entries map[string]*entry
	subs    map[chan Change]struct{}
}

// New prepares the state directory, writes the hook settings and rebuilds every open session from its events.
func New(ctx context.Context, cfg Config) (*Manager, error) {
	if err := cfg.Store.Init(); err != nil {
		return nil, err
	}
	// shortcut: prunes only when serve starts; a serve that runs for months keeps its archive meanwhile, then prune
	// from the pane-check ticker.
	if err := cfg.Store.PruneArchive(cfg.Now()); err != nil {
		cfg.Logger.Error("prune the archive", "err", err)
	}
	if err := writeHookSettings(cfg.Store.HookSettingsPath(), cfg.HookCommand); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, entries: map[string]*entry{}, subs: map[chan Change]struct{}{}}
	metas, err := cfg.Store.LoadMetas()
	if err != nil {
		return nil, err
	}
	for _, meta := range metas {
		e, err := m.load(ctx, meta)
		if err != nil {
			return nil, err
		}
		if e.ended() {
			if err := cfg.Store.Archive(meta.ID); err != nil {
				return nil, err
			}
			continue
		}
		m.entries[meta.ID] = e
	}
	return m, nil
}

// load rebuilds a session the app did not just start, from its metadata and the events recorded so far.
func (m *Manager) load(ctx context.Context, meta store.Meta) (*entry, error) {
	e := m.newEntry(meta)
	e.trustChecked = true
	e.branch = m.branchOf(ctx, meta)
	if _, err := m.readNew(e); err != nil {
		return nil, err
	}
	return e, nil
}

func (m *Manager) newEntry(meta store.Meta) *entry {
	e := &entry{meta: meta, state: state.New(meta.CreatedAt)}
	switch {
	case !meta.Discovered:
		e.pane, e.inTmux = pane{tmux: m.cfg.Tmux, target: meta.TmuxName}, true
	case meta.TmuxPane != "":
		e.pane, e.inTmux = pane{tmux: m.cfg.TmuxAt(meta.TmuxSocket), target: meta.TmuxPane}, true
	}
	return e
}

// ended reports whether e has ended: it leaves the list, and its conversation shows up in History. A discovered
// session also comes back if the global hook reports it again.
func (e *entry) ended() bool {
	return e.state.Kind == state.KindEnded
}

// tmuxOf returns the pane of a session the app can type into.
func (e *entry) tmuxOf() (pane, error) {
	if !e.inTmux {
		return pane{}, ErrNotInTmux
	}
	return e.pane, nil
}

// Run follows every session until ctx ends: new events, pending deliveries, the trust screen and vanished panes.
func (m *Manager) Run(ctx context.Context) {
	poll := time.NewTicker(m.cfg.PollInterval)
	defer poll.Stop()
	panes := time.NewTicker(m.cfg.PaneCheckInterval)
	defer panes.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			m.discover(ctx)
			m.pollEvents()
			m.checkPending(ctx)
			m.checkTrust(ctx)
			m.checkScreens(ctx)
		case <-panes.C:
			m.checkPanes(ctx)
		}
	}
}

func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]Info, 0, len(m.entries))
	for _, e := range m.entries {
		infos = append(infos, e.info(m.cfg.TicketPattern))
	}
	slices.SortFunc(infos, func(a, b Info) int { return strings.Compare(a.CreatedAt+a.ID, b.CreatedAt+b.ID) })
	return infos
}

func (m *Manager) Get(id string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return Info{}, ErrNotFound
	}
	return e.info(m.cfg.TicketPattern), nil
}

// Subscribe returns a stream of changes and a function that ends it. A subscriber that falls behind loses
// changes rather than slowing the app down; it can reload the list.
func (m *Manager) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, subscriberBuffer)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
	}
}

// Start runs claude in a new tmux session for the repository at req.Dir and sends req.Prompt as its first prompt.
func (m *Manager) Start(ctx context.Context, req StartRequest) (Info, error) {
	repo, err := m.repoAt(req.Dir)
	if err != nil {
		return Info{}, err
	}
	dir := repo.Dir
	if req.Worktree {
		if dir, err = m.addWorktree(ctx, repo, strings.TrimSpace(req.Branch)); err != nil {
			return Info{}, err
		}
	}
	branch := m.branchOf(ctx, store.Meta{Dir: dir})
	ticket := strings.TrimSpace(req.Ticket)
	if ticket == "" {
		ticket = TicketIn(m.cfg.TicketPattern, branch)
	}
	meta := store.Meta{Ticket: ticket, Title: strings.TrimSpace(req.Title), Repo: repo.Name, Dir: dir, Worktree: req.Worktree}
	return m.launch(ctx, meta, branch, m.claudeCommand(req.Prompt))
}

// addWorktree makes a worktree of repo on branch, next to the root the repository is in:
// <root>-worktrees/<repo>/<branch>. The app never removes it; git worktree remove does.
func (m *Manager) addWorktree(ctx context.Context, repo Repo, branch string) (string, error) {
	if repo.Workspace {
		return "", ErrNoWorktree
	}
	if branch == "" {
		return "", ErrNoBranch
	}
	root := filepath.Dir(repo.Dir)
	dir := filepath.Join(root+worktreesSuffix, repo.Name, unsafeNameChar.ReplaceAllString(branch, "_"))
	if err := m.cfg.AddWorktree(ctx, repo.Dir, dir, branch); err != nil {
		if errors.Is(err, gitdiff.ErrBadBranch) {
			return "", err
		}
		return "", fmt.Errorf("%w: %w", ErrWorktree, err)
	}
	return dir, nil
}

// launch saves a new app session with meta's ticket, title, repo and folder, and runs command for it in a new
// tmux session.
func (m *Manager) launch(ctx context.Context, meta store.Meta, branch string, command []string) (Info, error) {
	id, err := store.NewID()
	if err != nil {
		return Info{}, err
	}
	name, err := m.uniqueName(ctx, meta.Ticket, meta.Repo)
	if err != nil {
		return Info{}, err
	}
	meta.ID, meta.TmuxName, meta.CreatedAt = id, name, m.cfg.Now()
	if err := m.cfg.Store.SaveMeta(meta); err != nil {
		return Info{}, err
	}
	if err := m.startTmux(ctx, id, name, meta.Dir, command); err != nil {
		return Info{}, errors.Join(err, m.cfg.Store.Archive(id))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.newEntry(meta)
	e.branch = branch
	m.entries[id] = e
	m.publish(e, false, true)
	return e.info(m.cfg.TicketPattern), nil
}

func (m *Manager) startTmux(ctx context.Context, id, name, dir string, command []string) error {
	if err := m.cfg.Tmux.Start(ctx, tmux.NewSession{
		Name: name, Dir: dir, Env: map[string]string{hook.SessionEnv: id},
		Width: terminalWidth, Height: terminalHeight, Command: command,
	}); err != nil {
		return err
	}
	for option, value := range map[string]string{"status": "off", "mouse": "on"} {
		if err := m.cfg.Tmux.SetOption(ctx, name, option, value); err != nil {
			m.cfg.Logger.Error("set tmux option", "session", id, "option", option, "err", err)
		}
	}
	return nil
}

// Conversations lists the past conversations in Claude Code's transcripts that no listed session holds and whose
// folder still exists, latest first: the ones History can resume.
func (m *Manager) Conversations() ([]Conversation, error) {
	all, err := transcript.List(m.cfg.Transcripts)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	held := map[string]bool{}
	for id, e := range m.entries {
		held[id] = true
		held[e.state.ClaudeSessionID] = true
	}
	m.mu.Unlock()
	list := make([]Conversation, 0, len(all))
	folders := map[string]bool{}
	for _, c := range all {
		exists, checked := folders[c.Dir]
		if !checked {
			info, err := os.Stat(c.Dir)
			exists = err == nil && info.IsDir()
			folders[c.Dir] = exists
		}
		if held[c.ID] || !exists {
			continue
		}
		list = append(list, Conversation{
			ID: c.ID, Title: c.Title, Ticket: TicketIn(m.cfg.TicketPattern, c.Branch), Repo: filepath.Base(c.Dir),
			Dir: c.Dir, Branch: c.Branch, UpdatedAt: state.FormatTime(c.UpdatedAt),
		})
	}
	return list, nil
}

// Preview reads what a conversation was about from its transcript, for an ended session or one in History.
func (m *Manager) Preview(claudeSessionID string) (transcript.Preview, error) {
	p, err := transcript.ReadPreview(m.cfg.Transcripts, claudeSessionID)
	if errors.Is(err, transcript.ErrNotFound) {
		return transcript.Preview{}, ErrNotFound
	}
	return p, err
}

// ResumeConversation runs a past conversation from Claude Code's transcripts again, as a new app session in its
// folder, so the app can answer it.
func (m *Manager) ResumeConversation(ctx context.Context, claudeSessionID string) (Info, error) {
	c, err := transcript.Find(m.cfg.Transcripts, claudeSessionID)
	if errors.Is(err, transcript.ErrNotFound) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	if m.runsElsewhere(c.ID) {
		return Info{}, ErrStillRunning
	}
	if info, err := os.Stat(c.Dir); err != nil || !info.IsDir() {
		return Info{}, fmt.Errorf("%w: %s", ErrNoFolder, c.Dir)
	}
	branch := m.branchOf(ctx, store.Meta{Dir: c.Dir})
	meta := store.Meta{Ticket: TicketIn(m.cfg.TicketPattern, branch), Title: c.Title, Repo: filepath.Base(c.Dir), Dir: c.Dir}
	return m.launch(ctx, meta, branch, append(m.claudeBase(), resumeFlag, c.ID))
}

// Rename changes the title the page shows; an empty title brings back the folder name. Only Title is assigned on
// the entry: Close reads its other meta fields without m.mu.
func (m *Manager) Rename(id, title string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return Info{}, ErrNotFound
	}
	meta := e.meta
	meta.Title = strings.TrimSpace(title)
	if err := m.cfg.Store.SaveMeta(meta); err != nil {
		return Info{}, err
	}
	e.meta.Title = meta.Title
	m.publish(e, false, true)
	return e.info(m.cfg.TicketPattern), nil
}

// runsElsewhere reports whether a listed session that has not ended holds the conversation, such as one resumed
// from a terminal.
func (m *Manager) runsElsewhere(claudeSessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if !e.ended() && e.state.ClaudeSessionID == claudeSessionID {
			return true
		}
	}
	return false
}

// Close ends the session the way a user would (/exit), kills its tmux session if that does not work within a few
// seconds, and archives it. A discovered session is only hidden: the app did not start it, so it does not end it.
func (m *Manager) Close(ctx context.Context, id string) error {
	e, s, err := m.lookup(id)
	if err != nil {
		return err
	}
	e.send.Lock()
	defer e.send.Unlock()
	if e.meta.Discovered {
		return m.remove(e)
	}

	name := e.meta.TmuxName
	alive, err := m.cfg.Tmux.Has(ctx, name)
	if err != nil {
		return err
	}
	if alive && s.Kind != state.KindEnded {
		if err := m.exit(ctx, name, s.Kind == state.KindWorking); err != nil {
			m.cfg.Logger.Error("exit claude", "session", id, "err", err)
		}
		if alive, err = m.waitGone(ctx, name); err != nil {
			return err
		}
	}
	if alive {
		if err := m.cfg.Tmux.Kill(ctx, name); err != nil {
			return err
		}
	}
	if err := m.cfg.Store.AppendAppEvent(id, state.EventClosed, m.cfg.Now()); err != nil {
		return err
	}
	return m.remove(e)
}

// remove archives the session and takes it off the list.
func (m *Manager) remove(e *entry) error {
	if err := m.cfg.Store.Archive(e.meta.ID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, e.meta.ID)
	m.publish(e, true, true)
	return nil
}

// Attach returns the command line that attaches a terminal to the session.
func (m *Manager) Attach(id string) ([]string, error) {
	e, _, err := m.lookup(id)
	if err != nil {
		return nil, err
	}
	p, err := e.tmuxOf()
	if err != nil {
		return nil, err
	}
	return p.tmux.AttachCommand(p.target), nil
}

func (m *Manager) exit(ctx context.Context, name string, working bool) error {
	if working {
		if err := m.cfg.Tmux.SendKeys(ctx, name, keyInterrupt); err != nil {
			return err
		}
	}
	steps := []answer.Step{{Keys: []string{keyClearInput}}, {Text: exitCommand}, {Keys: []string{keyEnter}}}
	return m.execute(ctx, pane{tmux: m.cfg.Tmux, target: name}, steps)
}

func (m *Manager) waitGone(ctx context.Context, name string) (bool, error) {
	deadline := time.Now().Add(closeWait)
	for time.Now().Before(deadline) {
		alive, err := m.cfg.Tmux.Has(ctx, name)
		if err != nil || !alive {
			return alive, err
		}
		if err := sleep(ctx, closePoll); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (m *Manager) claudeCommand(prompt string) []string {
	cmd := m.claudeBase()
	if prompt == "" {
		return cmd
	}
	// A prompt starting with "-" would be read as an option; the leading space keeps it a prompt.
	if strings.HasPrefix(prompt, "-") {
		prompt = " " + prompt
	}
	return append(cmd, prompt)
}

func (m *Manager) claudeBase() []string {
	return []string{m.cfg.Claude, "--settings", m.cfg.Store.HookSettingsPath(), "--permission-mode", permissionModeAuto}
}

func (m *Manager) uniqueName(ctx context.Context, ticket, repo string) (string, error) {
	if ticket == "" {
		ticket = noTicket
	}
	base := TmuxBase(ticket, repo, m.cfg.StripPrefix)
	m.mu.Lock()
	taken := map[string]bool{}
	for _, e := range m.entries {
		taken[e.meta.TmuxName] = true
	}
	m.mu.Unlock()
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		if taken[name] {
			continue
		}
		exists, err := m.cfg.Tmux.Has(ctx, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
}

// branchOf reads the branch of the session's directory. Only a failure in a repository is an error: a discovered
// session may run in any folder, and a workspace session runs in a folder that is no repository at all.
func (m *Manager) branchOf(ctx context.Context, meta store.Meta) string {
	if meta.Dir == "" {
		return ""
	}
	branch, err := m.cfg.CurrentBranch(ctx, meta.Dir)
	if err != nil {
		level := slog.LevelDebug
		if !meta.Discovered && isGitRepo(meta.Dir) {
			level = slog.LevelError
		}
		m.cfg.Logger.Log(ctx, level, "read current branch", "dir", meta.Dir, "err", err)
		return ""
	}
	return branch
}

func (m *Manager) lookup(id string) (*entry, state.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, state.Session{}, ErrNotFound
	}
	return e, e.state, nil
}

// readNew applies the events written since the last read and reports whether anything visible changed.
// The caller holds m.mu, or owns e exclusively.
func (m *Manager) readNew(e *entry) (bool, error) {
	records, offset, err := m.cfg.Store.ReadEvents(e.meta.ID, e.offset)
	if err != nil {
		return false, err
	}
	e.offset = offset
	changed := false
	for _, rec := range records {
		seq := e.state.Seq
		e.state = state.Apply(e.state, rec)
		e.lastEventAt = rec.TS
		if e.state.Seq != seq {
			changed = true
		}
		// Any hook after a send proves Claude reacted, also one that comes after the send was given up on.
		if e.delivery != DeliveryNone && state.IsMainHook(rec) && (e.pending == nil || !rec.TS.Before(e.pending.sentAt)) {
			e.pending = nil
			e.delivery = DeliveryNone
			changed = true
		}
	}
	return changed, nil
}

func (m *Manager) pollEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.entries {
		changed, err := m.readNew(e)
		if err != nil {
			m.cfg.Logger.Error("read session events", "session", id, "err", err)
			continue
		}
		if e.ended() {
			if err := m.cfg.Store.Archive(id); err != nil {
				m.cfg.Logger.Error("archive ended session", "session", id, "err", err)
				continue
			}
			delete(m.entries, id)
			m.publish(e, true, true)
			continue
		}
		if changed {
			m.publish(e, false, true)
		}
	}
}

// discover adds the sessions the global hook has written metadata for since the last poll. Their events so far
// count as live: a session that is already waiting when it first appears should alert.
func (m *Manager) discover(ctx context.Context) {
	ids, err := m.cfg.Store.IDs()
	if err != nil {
		m.cfg.Logger.Error("list sessions", "err", err)
		return
	}
	m.mu.Lock()
	var unknown []string
	for _, id := range ids {
		if _, ok := m.entries[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	m.mu.Unlock()

	for _, id := range unknown {
		meta, err := m.cfg.Store.LoadMeta(id)
		if err != nil {
			m.cfg.Logger.Error("load discovered session", "session", id, "err", err)
			continue
		}
		// An app session that Start has saved but not listed yet.
		if !meta.Discovered {
			continue
		}
		e, err := m.load(ctx, meta)
		if err != nil {
			m.cfg.Logger.Error("load discovered session", "session", id, "err", err)
			continue
		}
		m.mu.Lock()
		if _, ok := m.entries[id]; !ok {
			m.entries[id] = e
			m.publish(e, false, true)
		}
		m.mu.Unlock()
	}
}

type paneCheck struct {
	id, name, branch string
	meta             store.Meta
}

func (m *Manager) checkPanes(ctx context.Context) {
	m.mu.Lock()
	var checks []paneCheck
	for id, e := range m.entries {
		if e.state.Kind != state.KindEnded {
			checks = append(checks, paneCheck{id: id, name: e.meta.TmuxName, branch: e.branch, meta: e.meta})
		}
	}
	m.mu.Unlock()

	for _, c := range checks {
		gone, event, err := m.gone(ctx, c)
		if err != nil {
			m.cfg.Logger.Error("check session is running", "session", c.id, "err", err)
			continue
		}
		if gone {
			if err := m.cfg.Store.AppendAppEvent(c.id, event, m.cfg.Now()); err != nil {
				m.cfg.Logger.Error("record session gone", "session", c.id, "event", event, "err", err)
			}
			continue
		}
		if branch := m.branchOf(ctx, c.meta); branch != c.branch {
			m.mu.Lock()
			if e, ok := m.entries[c.id]; ok {
				e.branch = branch
				m.publish(e, false, true)
			}
			m.mu.Unlock()
		}
	}
}

// gone reports whether the session no longer runs, and the event that records it. An app session is gone with its
// tmux session. A discovered session is gone with its claude process; without a known process only SessionEnd or
// Hide removes it.
func (m *Manager) gone(ctx context.Context, c paneCheck) (bool, string, error) {
	if !c.meta.Discovered {
		alive, err := m.cfg.Tmux.Has(ctx, c.name)
		return !alive, state.EventPaneGone, err
	}
	if c.meta.PID == 0 {
		return false, "", nil
	}
	return !m.cfg.Alive(proc.Process{PID: c.meta.PID, Start: c.meta.PIDStart}), state.EventProcessGone, nil
}

// checkTrust looks at the screen of a session that has been silent since it started: Claude Code shows the trust
// question before any hook can fire.
func (m *Manager) checkTrust(ctx context.Context) {
	now := m.cfg.Now()
	m.mu.Lock()
	var due []paneCheck
	for id, e := range m.entries {
		silent := e.state.Kind == state.KindIdle && e.state.ClaudeSessionID == ""
		if !e.trustChecked && silent && now.Sub(e.meta.CreatedAt) >= m.cfg.TrustWait {
			e.trustChecked = true
			due = append(due, paneCheck{id: id, name: e.meta.TmuxName})
		}
	}
	m.mu.Unlock()

	for _, c := range due {
		screen, err := m.cfg.Tmux.Capture(ctx, c.name)
		if err != nil {
			m.cfg.Logger.Error("capture screen for trust check", "session", c.id, "err", err)
			continue
		}
		if answer.IsTrustPrompt(screen) {
			if err := m.cfg.Store.AppendAppEvent(c.id, state.EventTrustPrompt, now); err != nil {
				m.cfg.Logger.Error("record trust prompt", "session", c.id, "err", err)
			}
		}
	}
}

// checkScreens reads the screen of each session in tmux that has been quiet for a while and waits on nothing a hook
// reported. Claude Code asks some things without any hook (a new MCP server, a usage limit); such a menu shows as
// a question read from the screen, and goes away when it leaves the screen.
func (m *Manager) checkScreens(ctx context.Context) {
	now := m.cfg.Now()
	type check struct {
		e *entry
		s state.Session
	}
	var due []check
	m.mu.Lock()
	for _, e := range m.entries {
		hooked := e.state.NeedsYou() && !e.state.FromScreen
		if !e.inTmux || e.pending != nil || hooked || e.ended() ||
			now.Sub(e.lastEventAt) < m.cfg.ScreenCheckInterval || now.Sub(e.screenCheckedAt) < m.cfg.ScreenCheckInterval {
			continue
		}
		e.screenCheckedAt = now
		due = append(due, check{e: e, s: e.state})
	}
	m.mu.Unlock()

	for _, c := range due {
		screen, err := c.e.pane.tmux.Capture(ctx, c.e.pane.target)
		if err != nil {
			m.cfg.Logger.Error("capture screen to look for a menu", "session", c.e.meta.ID, "err", err)
			continue
		}
		menu, ok := answer.ParseMenu(screen)
		switch {
		case ok && (!c.s.FromScreen || !sameAsAsked(c.s, menu)):
			err = m.appendAppEvent(c.e.meta.ID, state.EventScreenPrompt, menu.AsQuestion())
		case !ok && c.s.FromScreen:
			err = m.appendAppEvent(c.e.meta.ID, state.EventScreenPromptGone, state.SeqPayload{Seq: c.s.Seq})
		}
		if err != nil {
			m.cfg.Logger.Error("record a menu read from the screen", "session", c.e.meta.ID, "err", err)
		}
	}
}

func sameAsAsked(s state.Session, menu answer.Menu) bool {
	asked := answer.Menu{Question: s.Questions[0].Question, Options: s.Questions[0].Options}
	return asked.Same(menu)
}

// publish sends e to every subscriber. The caller holds m.mu.
func (m *Manager) publish(e *entry, removed, live bool) {
	change := Change{Session: e.info(m.cfg.TicketPattern), Removed: removed, Live: live}
	for ch := range m.subs {
		select {
		case ch <- change:
		default:
		}
	}
}

func (e *entry) info(ticketPattern *regexp.Regexp) Info {
	info := Info{
		ID: e.meta.ID, Ticket: e.meta.Ticket, Title: e.meta.Title, Repo: e.meta.Repo, Dir: e.meta.Dir,
		TmuxName: e.meta.TmuxName, Branch: e.branch, CreatedAt: state.FormatTime(e.meta.CreatedAt),
		State: e.state.View(), Delivery: e.delivery, Discovered: e.meta.Discovered, Worktree: e.meta.Worktree, InTmux: e.inTmux,
		ClaudeSessionID: e.state.ClaudeSessionID,
	}
	if e.meta.Discovered {
		info.Ticket = TicketIn(ticketPattern, e.branch)
	}
	if e.inTmux {
		info.Attach = strings.Join(e.pane.tmux.AttachCommand(e.pane.target), " ")
	}
	return info
}

// TicketIn returns the first ticket id in branch, or "" when pattern is nil or finds none.
func TicketIn(pattern *regexp.Regexp, branch string) string {
	if pattern == nil {
		return ""
	}
	return pattern.FindString(branch)
}

// TmuxBase is the tmux session name for a session on ticket in repo, before any -2, -3 suffix: <ticket>-<repo>,
// or just <repo> without a ticket, with stripPrefix left out of the repository name. claude-tmux names its
// sessions with it too (claude-inbox tmux-name).
func TmuxBase(ticket, repo, stripPrefix string) string {
	name := strings.TrimPrefix(repo, stripPrefix)
	if ticket != "" {
		name = ticket + "-" + name
	}
	return unsafeNameChar.ReplaceAllString(name, "_")
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
