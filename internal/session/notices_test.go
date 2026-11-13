package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const noticeHead = "[from claude-inbox] "

// groupFixture starts a group of a coordinator and one worker, with the tmux keys of the start taken.
func groupFixture(t *testing.T) (*fixture, Info, Info) {
	t.Helper()
	f := newFixture(t)
	infos, err := f.m.StartGroup(context.Background(), StartGroupRequest{Dirs: []string{f.repo}, Ticket: "PROJ-5", Coordinator: true})
	require.NoError(t, err)
	f.tmux.takeSent()
	return f, infos[0], infos[1]
}

func noticeTexts(f *fixture) []string {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	texts := make([]string, len(f.m.notices))
	for i, n := range f.m.notices {
		texts[i] = n.text
	}
	return texts
}

func TestUnreportedUserTurnIsToldToTheCoordinator(t *testing.T) {
	f, coordinator, worker := groupFixture(t)
	ctx := context.Background()
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "Rename the\nexport"})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Renamed it."})

	f.m.pollEvents()
	f.m.deliverNotices(ctx)

	want := noticeHead + worker.TmuxName + " finished a turn the user started directly, without telling you. " +
		"The user asked: «Rename the export». Its last message: «Renamed it.»"
	assert.Equal(t, []string{keyClearInput, textPrefix + want, keyEnter}, f.tmux.takeSent())
	assert.Equal(t, coordinator.TmuxName, f.tmux.targets[len(f.tmux.targets)-1])
	assert.Empty(t, noticeTexts(f))
	msgs, err := f.m.GroupMessages(coordinator.Group)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, noticeSender, msgs[0].From)
}

func TestReportedTurnMakesNoNotice(t *testing.T) {
	f, coordinator, worker := groupFixture(t)
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "[from " + coordinator.TmuxName + "] Add the claim."})
	f.clock.Advance(time.Second)
	_, err := f.m.Send(context.Background(), worker.ID, coordinator.TmuxName, "Done.")
	require.NoError(t, err)
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Done."})

	f.m.pollEvents()

	assert.Empty(t, noticeTexts(f))
}

func TestUnansweredGroupMessageFailureAndEndAreToldToTheCoordinator(t *testing.T) {
	f, coordinator, worker := groupFixture(t)
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "[from " + coordinator.TmuxName + "] Add the claim."})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Added."})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "go on"})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "StopFailure", "last_assistant_message": "API Error: Connection error."})
	require.NoError(t, f.store.AppendAppEvent(worker.ID, state.EventPaneGone, f.clock.Now()))

	f.m.pollEvents()

	assert.Equal(t, []string{
		noticeHead + worker.TmuxName + " finished the turn a group message started without replying. Its last message: «Added.»",
		noticeHead + worker.TmuxName + " stopped on an error. Its last message: «API Error: Connection error.»",
		noticeHead + worker.TmuxName + " has ended: its session is gone, so it will not report again.",
	}, noticeTexts(f))
}

func TestStalledWorkerNeedsYouAndIsToldToTheCoordinator(t *testing.T) {
	f, _, worker := groupFixture(t)
	f.m.cfg.StallAfter = 5 * time.Minute
	alone := f.start(t, "PROJ-6", "")
	for _, id := range []string{worker.ID, alone.ID} {
		f.hook(t, id, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "make test"}})
	}
	f.m.pollEvents()

	f.clock.Advance(4 * time.Minute)
	f.m.checkStalls()
	f.m.pollEvents()
	assert.False(t, f.current(t, worker.ID).State.Stalled, "not silent long enough yet")

	f.clock.Advance(time.Minute)
	f.m.checkStalls()
	f.m.pollEvents()

	view := f.current(t, worker.ID).State
	assert.True(t, view.Stalled)
	assert.True(t, view.NeedsYou)
	assert.False(t, f.current(t, alone.ID).State.Stalled, "only a group's sessions stall")
	assert.Equal(t, []string{noticeHead + worker.TmuxName + " has been working with no sign of activity for 5m0s; it may have lost its connection. The user was alerted."}, noticeTexts(f))

	f.m.checkStalls()
	f.m.pollEvents()
	assert.Len(t, noticeTexts(f), 1, "one stall, one notice")
}

func TestNoticeWaitsWhileTheCoordinatorNeedsYou(t *testing.T) {
	f, coordinator, worker := groupFixture(t)
	ctx := context.Background()
	f.hook(t, coordinator.ID, askBash("make test"))
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "Fix it"})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Fixed."})
	f.m.pollEvents()

	f.m.deliverNotices(ctx)
	assert.Empty(t, f.tmux.takeSent())
	assert.Len(t, noticeTexts(f), 1, "kept for later")

	f.hook(t, coordinator.ID, map[string]any{"hook_event_name": "PostToolUse"})
	f.m.pollEvents()
	f.m.deliverNotices(ctx)
	sent := f.tmux.takeSent()
	require.Len(t, sent, 2, "a working coordinator gets the text and Enter, no Ctrl+C")
	assert.Contains(t, sent[0], "finished a turn the user started directly")
	assert.Empty(t, noticeTexts(f))
}

func TestReplayMakesNoNotices(t *testing.T) {
	f, _, worker := groupFixture(t)
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "Fix it"})
	f.hook(t, worker.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Fixed."})

	restarted, err := New(context.Background(), f.cfg)

	require.NoError(t, err)
	assert.Empty(t, restarted.notices)
}
