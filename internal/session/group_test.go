package session

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

const secondRepoName = "acme-wallet"

// secondRepo adds a second repository next to the fixture's one.
func (f *fixture) secondRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(f.repo), secondRepoName)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	return dir
}

func flagValues(cmd []string, flag string) []string {
	i := slices.Index(cmd, flag)
	if i < 0 {
		return nil
	}
	var values []string
	for _, v := range cmd[i+1:] {
		if len(v) > 1 && v[:2] == "--" {
			break
		}
		values = append(values, v)
	}
	return values
}

func TestStartGroupWithCoordinator(t *testing.T) {
	f := newFixture(t)
	wallet := f.secondRepo(t)
	branch, prompt := "PROJ-7-device-id", "Add device_id to the login token."
	f.branch = branch

	infos, err := f.m.StartGroup(context.Background(), StartGroupRequest{
		Dirs: []string{wallet, f.repo}, Ticket: "PROJ-7", Title: "Device id", Prompt: prompt,
		Worktree: true, Branch: branch, Coordinator: true, ShareDirs: true,
	})

	require.NoError(t, err)
	require.Len(t, infos, 3)
	root := filepath.Dir(f.repo)
	coordinator, contact, walletInfo := infos[0], infos[1], infos[2]
	assert.Equal(t, RoleCoordinator, coordinator.Role)
	assert.Equal(t, root, coordinator.Dir, "the coordinator runs in the folder that holds the repositories")
	assert.Equal(t, "PROJ-7", coordinator.Ticket)
	contactDir := filepath.Join(root+worktreesSuffix, repoName, branch)
	walletDir := filepath.Join(root+worktreesSuffix, secondRepoName, branch)
	assert.Equal(t, contactDir, contact.Dir)
	assert.Equal(t, walletDir, walletInfo.Dir)
	for _, info := range infos {
		assert.Equal(t, coordinator.Group, info.Group)
		assert.Equal(t, "Device id", info.Title)
	}
	assert.NotEmpty(t, coordinator.Group)
	assert.Equal(t, RoleWorker, contact.Role)
	assert.Equal(t, "PROJ-7-contact", contact.TmuxName)
	assert.Equal(t, "PROJ-7-wallet", walletInfo.TmuxName)

	require.Len(t, f.tmux.started, 3)
	coordCmd := f.tmux.started[0].Command
	assert.Equal(t, []string{contactDir, walletDir}, flagValues(coordCmd, addDirFlag))
	assert.Equal(t, prompt, coordCmd[len(coordCmd)-1], "only the coordinator gets the task")
	roster := coordCmd[slices.Index(coordCmd, systemPromptFlag)+1]
	assert.Equal(t, "PROJ-7-coordinator", coordinator.TmuxName)
	assert.Contains(t, roster, "You are "+coordinator.TmuxName+", the coordinator")
	assert.Contains(t, roster, contact.TmuxName)
	assert.Contains(t, roster, "/home/user/.local/bin/claude-inbox send --state-dir "+f.store.Root()+" <name>")

	contactCmd := f.tmux.started[1].Command
	assert.Equal(t, []string{root, walletDir}, flagValues(contactCmd, addDirFlag))
	assert.Equal(t, systemPromptFlag, contactCmd[len(contactCmd)-2], "a worker gets no first prompt")
	assert.Contains(t, contactCmd[len(contactCmd)-1], "Wait for your part from "+coordinator.TmuxName)
}

func TestStartGroupWithoutCoordinator(t *testing.T) {
	f := newFixture(t)
	wallet := f.secondRepo(t)
	prompt := "Rename the claim everywhere."

	infos, err := f.m.StartGroup(context.Background(), StartGroupRequest{Dirs: []string{f.repo, wallet, f.repo}, Ticket: "PROJ-8", Prompt: prompt})

	require.NoError(t, err)
	require.Len(t, infos, 2, "a repository picked twice starts once")
	for i, started := range f.tmux.started {
		assert.Equal(t, RoleWorker, infos[i].Role)
		assert.Nil(t, flagValues(started.Command, addDirFlag), "folders are shared only when asked")
		assert.Equal(t, prompt, started.Command[len(started.Command)-1], "without a coordinator every session gets the task")
		assert.Contains(t, started.Command[slices.Index(started.Command, systemPromptFlag)+1], "There is no coordinator")
	}
}

func TestStartGroupRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.m.StartGroup(ctx, StartGroupRequest{})
	require.ErrorIs(t, err, ErrNoRepos)

	_, err = f.m.StartGroup(ctx, StartGroupRequest{Dirs: []string{f.repo, "/etc"}})
	require.ErrorIs(t, err, ErrInvalidRepo)

	_, err = f.m.StartGroup(ctx, StartGroupRequest{Dirs: []string{f.repo}, Worktree: true})
	require.ErrorIs(t, err, ErrNoBranch)

	assert.Empty(t, f.tmux.started, "nothing starts when one repository cannot")
}

func TestSendInGroup(t *testing.T) {
	f := newFixture(t)
	wallet := f.secondRepo(t)
	ctx := context.Background()
	infos, err := f.m.StartGroup(ctx, StartGroupRequest{Dirs: []string{f.repo, wallet}, Ticket: "PROJ-9", Coordinator: true})
	require.NoError(t, err)
	coordinator, contact := infos[0], infos[1]
	f.tmux.takeSent()
	text := "Done: claim added, commit a1c9e04."

	_, err = f.m.Send(ctx, contact.ID, coordinator.TmuxName, text)

	require.NoError(t, err)
	assert.Equal(t, []string{keyClearInput, textPrefix + "[from " + contact.TmuxName + "] " + text, keyEnter}, f.tmux.takeSent())
	msgs, err := f.m.GroupMessages(coordinator.Group)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, store.GroupMessage{SentAt: f.clock.Now(), From: contact.TmuxName, To: coordinator.TmuxName, Text: text}, msgs[0])
}

func TestSendRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	infos, err := f.m.StartGroup(ctx, StartGroupRequest{Dirs: []string{f.repo}, Ticket: "PROJ-10", Coordinator: true})
	require.NoError(t, err)
	coordinator, contact := infos[0], infos[1]
	alone := f.start(t, "PROJ-11", "")

	_, err = f.m.Send(ctx, contact.ID, coordinator.TmuxName, " ")
	require.ErrorIs(t, err, answer.ErrEmptyText)
	_, err = f.m.Send(ctx, "nobody12", coordinator.TmuxName, "hi")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = f.m.Send(ctx, alone.ID, coordinator.TmuxName, "hi")
	require.ErrorIs(t, err, ErrNoGroup)
	_, err = f.m.Send(ctx, contact.ID, alone.TmuxName, "hi")
	require.ErrorIs(t, err, ErrNotInGroup, "a session outside the group")
	_, err = f.m.Send(ctx, contact.ID, contact.TmuxName, "hi")
	require.ErrorIs(t, err, ErrNotInGroup, "itself")

	_, err = f.m.GroupMessages("../evil1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCommonDir(t *testing.T) {
	assert.Equal(t, "/work/acme", commonDir([]string{"/work/acme/contact", "/work/acme/wallet"}))
	assert.Equal(t, "/work", commonDir([]string{"/work/acme/contact", "/work/libs/scripts"}))
	assert.Equal(t, "/work/acme", commonDir([]string{"/work/acme/contact"}))
	assert.Equal(t, "/work", commonDir([]string{"/work/acme-x/contact", "/work/acme/wallet"}), "a shared name prefix is not a shared folder")
}
