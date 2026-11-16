package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/settings"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"

	addDirFlag       = "--add-dir"
	systemPromptFlag = "--append-system-prompt"
)

var (
	// plainArg needs no quotes in a shell command line.
	plainArg = regexp.MustCompile(`^[A-Za-z0-9_./=-]+$`)

	ErrNoRepos = errors.New("a group needs at least one repository")
	// ErrTicketsWorktree: several tickets share each folder, so they cannot each have a worktree on one branch.
	ErrTicketsWorktree = errors.New("several tickets share one folder, so they take no worktree")
	// ErrNoGroup: the sending session was not started in a group.
	ErrNoGroup = errors.New("the session is not in a group")
	// ErrNotInGroup: no session of the sender's group has that name.
	ErrNotInGroup = errors.New("no session of that name in the group")
)

// StartGroupRequest starts one session per repository in Dirs, all on one ticket, and optionally a coordinator
// that gets Prompt and hands the work out (docs/architecture.md, "Session groups").
type StartGroupRequest struct {
	Dirs   []string `json:"dirs"`
	Ticket string   `json:"ticket"`
	// Tickets, when set, starts one session per ticket in every folder of Dirs, each on its ticket, instead of
	// one session per folder on Ticket; Ticket is then the coordinator's.
	Tickets []string `json:"tickets"`
	Title  string   `json:"title"`
	Prompt string   `json:"prompt"`
	// Worktree starts each session in a new git worktree of its repository, on Branch.
	Worktree bool   `json:"worktree"`
	Branch   string `json:"branch"`
	// Coordinator adds a session in the folder that holds the repositories; it gets Prompt instead of the others.
	Coordinator bool `json:"coordinator"`
	// ShareDirs lets every session read the folders of the others.
	ShareDirs bool `json:"shareDirs"`
}

type member struct {
	meta   store.Meta
	branch string
}

// StartGroup starts the sessions of a group. Every worktree is made before any session starts, so a bad branch
// starts nothing; a session that then fails to start leaves the others running, and its error is returned.
func (m *Manager) StartGroup(ctx context.Context, req StartGroupRequest) ([]Info, error) {
	dirs := slices.Compact(slices.Sorted(slices.Values(req.Dirs)))
	if len(dirs) == 0 {
		return nil, ErrNoRepos
	}
	var tickets []string
	for _, t := range req.Tickets {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(tickets, t) {
			tickets = append(tickets, t)
		}
	}
	req.Tickets = tickets
	if req.Worktree && len(req.Tickets) > 1 {
		return nil, ErrTicketsWorktree
	}
	groupID, err := store.NewID()
	if err != nil {
		return nil, err
	}
	members, repoDirs, err := m.groupMembers(ctx, dirs, req, groupID)
	if err != nil {
		return nil, err
	}
	if req.Coordinator {
		dir := commonDir(repoDirs)
		if len(req.Tickets) > 0 && len(dirs) == 1 {
			// The workers share the folder, so the coordinator runs there too, with its CLAUDE.md and skills.
			dir = repoDirs[0]
		}
		coordinator := member{
			meta:   store.Meta{Title: strings.TrimSpace(req.Title), Repo: filepath.Base(dir), Dir: dir, Group: groupID, Role: RoleCoordinator},
			branch: m.branchOf(ctx, store.Meta{Dir: dir}),
		}
		coordinator.meta.Ticket = members[0].meta.Ticket
		if len(req.Tickets) > 0 {
			coordinator.meta.Ticket = strings.TrimSpace(req.Ticket)
		}
		members = append([]member{coordinator}, members...)
	}
	reserved := map[string]bool{}
	for i := range members {
		if members[i].meta, err = m.reserve(ctx, members[i].meta, reserved); err != nil {
			return nil, err
		}
	}

	infos := make([]Info, 0, len(members))
	var errs []error
	for _, mb := range members {
		prompt := req.Prompt
		if req.Coordinator && mb.meta.Role != RoleCoordinator {
			prompt = ""
		}
		info, err := m.run(ctx, mb.meta, mb.branch, m.groupCommand(mb, members, req.ShareDirs, prompt))
		if err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", mb.meta.TmuxName, err))
			continue
		}
		infos = append(infos, info)
	}
	return infos, errors.Join(errs...)
}

// groupMembers resolves the workers, making their worktrees, and returns them with their repositories' folders.
func (m *Manager) groupMembers(ctx context.Context, dirs []string, req StartGroupRequest, groupID string) ([]member, []string, error) {
	tickets := req.Tickets
	if len(tickets) == 0 {
		tickets = []string{strings.TrimSpace(req.Ticket)}
	}
	members := make([]member, 0, len(dirs)*len(tickets)+1)
	repoDirs := make([]string, 0, len(dirs))
	for _, d := range dirs {
		repo, err := m.repoAt(d)
		if err != nil {
			return nil, nil, err
		}
		dir := repo.Dir
		if req.Worktree {
			if dir, err = m.addWorktree(ctx, repo, strings.TrimSpace(req.Branch)); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
		branch := m.branchOf(ctx, store.Meta{Dir: dir})
		for _, ticket := range tickets {
			if ticket == "" {
				ticket = TicketIn(m.cfg.TicketPattern, branch)
			}
			members = append(members, member{
				meta:   store.Meta{Ticket: ticket, Title: strings.TrimSpace(req.Title), Repo: repo.Name, Dir: dir, Worktree: req.Worktree, Group: groupID, Role: RoleWorker},
				branch: branch,
			})
		}
		repoDirs = append(repoDirs, repo.Dir)
	}
	return members, repoDirs, nil
}

// groupCommand is claude with the other members' folders readable and the group described in the system prompt.
// --add-dir takes several values, so the system prompt, which takes one, sits between them and the first prompt.
func (m *Manager) groupCommand(self member, members []member, shareDirs bool, prompt string) []string {
	cmd := m.claudeBase()
	if shareDirs {
		var others []string
		for _, mb := range members {
			if mb.meta.ID != self.meta.ID && !slices.Contains(others, mb.meta.Dir) && mb.meta.Dir != self.meta.Dir {
				others = append(others, mb.meta.Dir)
			}
		}
		if len(others) > 0 {
			cmd = append(append(cmd, addDirFlag), others...)
		}
	}
	cmd = append(cmd, systemPromptFlag, m.groupPrompt(self, members, shareDirs))
	return appendPrompt(cmd, prompt)
}

// groupPrompt tells a session who else is in its group, what its part is, and how to reach the others.
func (m *Manager) groupPrompt(self member, members []member, shareDirs bool) string {
	var coordinator string
	for _, mb := range members {
		if mb.meta.Role == RoleCoordinator {
			coordinator = mb.meta.TmuxName
		}
	}
	quoted := make([]string, len(m.cfg.SendCommand))
	for i, arg := range m.cfg.SendCommand {
		quoted[i] = arg
		if !plainArg.MatchString(arg) {
			quoted[i] = settings.Quote(arg)
		}
	}
	send := strings.Join(quoted, " ")

	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, the %s of a group of Claude Code sessions that claude-inbox started together to work on one task", self.meta.TmuxName, self.meta.Role)
	if self.meta.Ticket != "" {
		fmt.Fprintf(&b, " (%s)", self.meta.Ticket)
	}
	b.WriteString(". The sessions of the group:\n")
	for _, mb := range members {
		you := ""
		if mb.meta.ID == self.meta.ID {
			you = " (you)"
		}
		fmt.Fprintf(&b, "- %s%s: %s, %s, in %s\n", mb.meta.TmuxName, you, mb.meta.Role, mb.meta.Repo, mb.meta.Dir)
	}
	if shareDirs {
		b.WriteString("You can read the other sessions' folders. Change files only in your own folder.\n")
	}
	fmt.Fprintf(&b, "To message another session, run: %s <name> \"<text>\". It reaches that session as a prompt that starts with [from %s]; "+
		"a busy session gets it when its turn ends. Messages you receive start with [from <name>].\n", send, self.meta.TmuxName)
	switch {
	case self.meta.Role == RoleCoordinator:
		b.WriteString("You coordinate: split the task, send each worker its part, answer their questions, wait for their reports, " +
			"check that their changes fit together, then write the user one short summary. Do not change the workers' files yourself.\n" +
			"claude-inbox itself messages you, as [from claude-inbox], when a worker finishes a turn without reporting it (also one the user " +
			"started by typing to the worker directly), stops on an error, ends, or goes silent while working. Take those into account " +
			"before you report to the user.\n")
	case coordinator != "":
		fmt.Fprintf(&b, "Wait for your part from %s. After every turn in which you changed something, whoever asked for it (%s, another "+
			"session, or the user typing to you directly), tell %s in a short message: what changed, the commit, the tests. "+
			"Tell %s too when you get blocked. A turn you do not report, claude-inbox reports to %s for you.\n",
			coordinator, coordinator, coordinator, coordinator, coordinator)
	default:
		b.WriteString("There is no coordinator: when your change affects another session's part, tell that session.\n")
	}
	b.WriteString("Permission prompts go to the user, never to another session.")
	return b.String()
}

// commonDir is the deepest folder that holds every one of dirs.
func commonDir(dirs []string) string {
	common := filepath.Dir(dirs[0])
	for _, d := range dirs[1:] {
		for common != filepath.Dir(common) && !strings.HasPrefix(d, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
		}
	}
	return common
}

// Send delivers text from one session of a group to another, found by tmux name or id, and logs it for the
// group. The text arrives prefixed with the sender's name, so the receiver knows whom to answer.
func (m *Manager) Send(ctx context.Context, fromID, to, text string) (Info, error) {
	if strings.TrimSpace(text) == "" {
		return Info{}, answer.ErrEmptyText
	}
	m.mu.Lock()
	from, ok := m.entries[fromID]
	var target *entry
	if ok && from.meta.Group != "" {
		for _, e := range m.entries {
			if e.meta.Group == from.meta.Group && e != from && (e.meta.TmuxName == to || e.meta.ID == to) {
				target = e
			}
		}
	}
	m.mu.Unlock()
	switch {
	case !ok:
		return Info{}, ErrNotFound
	case from.meta.Group == "":
		return Info{}, ErrNoGroup
	case target == nil:
		return Info{}, fmt.Errorf("%w: %q", ErrNotInGroup, to)
	}
	info, err := m.Message(ctx, target.meta.ID, groupPrefix+from.meta.TmuxName+"] "+text)
	if err != nil {
		return Info{}, err
	}
	now := m.cfg.Now()
	m.mu.Lock()
	from.lastSentAt = now
	m.mu.Unlock()
	msg := store.GroupMessage{SentAt: now, From: from.meta.TmuxName, To: target.meta.TmuxName, Text: text}
	if err := m.cfg.Store.AppendGroupMessage(from.meta.Group, msg); err != nil {
		m.cfg.Logger.Error("log a group message", "group", from.meta.Group, "err", err)
	}
	return info, nil
}

// GroupMessages returns what the sessions of a group sent each other, oldest first.
func (m *Manager) GroupMessages(group string) ([]store.GroupMessage, error) {
	msgs, err := m.cfg.Store.GroupMessages(group)
	if errors.Is(err, store.ErrInvalidID) {
		return nil, ErrNotFound
	}
	return msgs, err
}
