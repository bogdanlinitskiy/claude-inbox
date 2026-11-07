package state

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/store"
)

const spikeFixture = "../../testdata/spike-hooks.jsonl"

var start = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T) []store.Record {
	t.Helper()
	f, err := os.Open(spikeFixture)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // read-only fixture

	var records []store.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var rec store.Record
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &rec))
		records = append(records, rec)
	}
	require.NoError(t, scanner.Err())
	return records
}

// The recorded run: a question, a Bash permission in manual mode, a free-text answer, an interrupt
// that fires no hook, then exit. Lines 8 and 9 come from an internal agent and must not change anything.
func TestReplaySpikeRun(t *testing.T) {
	want := []Kind{
		KindIdle, KindWorking, KindWorking, KindQuestion, KindQuestion, KindWorking, KindDone,
		KindDone, KindDone,
		KindWorking, KindWorking, KindPermission, KindPermission, KindWorking, KindDone,
		KindWorking, KindWorking, KindQuestion, KindWorking, KindDone, KindDone,
		KindWorking, KindEnded,
	}
	records := loadFixture(t)
	require.Len(t, records, len(want))

	s := New(start)
	for i, rec := range records {
		s = Apply(s, rec)
		require.Equal(t, want[i], s.Kind, "after fixture line %d", i+1)

		switch i + 1 {
		case 4:
			require.Len(t, s.Questions, 1)
			assert.Equal(t, "Which color do you prefer?", s.Questions[0].Question)
			assert.Equal(t, []Option{
				{Label: "Red", Description: "The color red"},
				{Label: "Green", Description: "The color green"},
				{Label: "Blue", Description: "The color blue"},
			}, s.Questions[0].Options)
			assert.True(t, s.View().CanAnswer)
			assert.True(t, s.NeedsYou())
		case 7:
			assert.Equal(t, "Green.", s.LastMessage)
		case 9:
			assert.Equal(t, "Green.", s.LastMessage, "the internal agent must not touch the state")
		case 12:
			require.NotNil(t, s.Permission)
			assert.Equal(t, Permission{
				Tool:        "Bash",
				Detail:      "mkdir -p sandbox && date > sandbox/now.txt",
				Description: "Create sandbox dir and write current date to file",
			}, *s.Permission)
			assert.Equal(t, "default", s.PermissionMode)
		case 14:
			assert.Nil(t, s.Permission)
		}
	}
	assert.Equal(t, "4e346dee-52b7-4567-9ea8-609b4407028e", s.ClaudeSessionID)
}

func TestReplayIsDeterministic(t *testing.T) {
	records := loadFixture(t)
	replay := func() Session {
		s := New(start)
		for _, rec := range records {
			s = Apply(s, rec)
		}
		return s
	}
	assert.Equal(t, replay(), replay())
}

func hookRecord(t *testing.T, at time.Time, fields map[string]any) store.Record {
	t.Helper()
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	return store.Record{TS: at, Source: store.SourceHook, Payload: body}
}

func appRecord(at time.Time, event string) store.Record {
	return store.Record{TS: at, Source: store.SourceApp, Event: event}
}

// /clear and /resume fire SessionEnd and then SessionStart; a poll can read the first alone.
func TestSessionEndOfClearOrResumeKeepsTheSession(t *testing.T) {
	done := Session{Kind: KindDone, Since: start, Seq: 4, ClaudeSessionID: "old"}
	for _, reason := range []string{endReasonClear, endReasonResume} {
		s := Apply(done, hookRecord(t, start.Add(time.Minute), map[string]any{"hook_event_name": hookSessionEnd, "reason": reason}))
		assert.Equal(t, done, s, reason)
	}

	exited := Apply(done, hookRecord(t, start.Add(time.Minute), map[string]any{"hook_event_name": hookSessionEnd, "reason": "prompt_input_exit"}))
	assert.Equal(t, KindEnded, exited.Kind)
}

func TestApply(t *testing.T) {
	at := start.Add(time.Minute)
	working := Session{Kind: KindWorking, Since: start, Seq: 3, Activity: "Bash: run tests"}
	question := Session{Kind: KindQuestion, Since: start, Seq: 5, Questions: []Question{{Question: "Which?"}}}
	ended := Session{Kind: KindEnded, Since: start, Seq: 9}

	cases := []struct {
		name     string
		from     Session
		rec      func(t *testing.T) store.Record
		wantKind Kind
		check    func(t *testing.T, s Session)
	}{
		{
			name: "event from an internal agent changes nothing",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "Stop", "agent_id": "a1", "last_assistant_message": "x"})
			},
			wantKind: KindWorking,
			check:    func(t *testing.T, s Session) { assert.Equal(t, working, s) },
		},
		{
			name:     "interrupt makes a working session idle",
			from:     working,
			rec:      func(*testing.T) store.Record { return appRecord(at, EventInterrupt) },
			wantKind: KindIdle,
			check:    func(t *testing.T, s Session) { assert.Empty(t, s.Activity) },
		},
		{
			name: "deny makes a permission request idle",
			from: Session{Kind: KindPermission, Since: start, Permission: &Permission{Tool: "Bash", Detail: "date"}},
			rec:  func(*testing.T) store.Record { return appRecord(at, EventDenied) },
			check: func(t *testing.T, s Session) {
				assert.Nil(t, s.Permission)
			},
			wantKind: KindIdle,
		},
		{
			name:     "trust prompt needs the user",
			from:     New(start),
			rec:      func(*testing.T) store.Record { return appRecord(at, EventTrustPrompt) },
			wantKind: KindTrust,
			check:    func(t *testing.T, s Session) { assert.True(t, s.NeedsYou()) },
		},
		{
			name:     "pane gone ends the session",
			from:     question,
			rec:      func(*testing.T) store.Record { return appRecord(at, EventPaneGone) },
			wantKind: KindEnded,
		},
		{
			name:     "claude process gone ends the session",
			from:     question,
			rec:      func(*testing.T) store.Record { return appRecord(at, EventProcessGone) },
			wantKind: KindEnded,
		},
		{
			name: "question that does not parse is unknown",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": "nope"}})
			},
			wantKind: KindUnknown,
		},
		{
			name: "question with several parts cannot be answered with buttons",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
					"questions": []map[string]any{{"question": "A?"}, {"question": "B?"}},
				}})
			},
			wantKind: KindQuestion,
			check:    func(t *testing.T, s Session) { assert.False(t, s.View().CanAnswer) },
		},
		{
			name: "multi-select question cannot be answered with buttons",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
					"questions": []map[string]any{{"question": "A?", "multiSelect": true}},
				}})
			},
			wantKind: KindQuestion,
			check:    func(t *testing.T, s Session) { assert.False(t, s.View().CanAnswer) },
		},
		{
			name: "permission for a file tool shows the path",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Write", "tool_input": map[string]any{"file_path": "/repo/a.go", "content": "package a"}})
			},
			wantKind: KindPermission,
			check: func(t *testing.T, s Session) {
				require.NotNil(t, s.Permission)
				assert.Equal(t, "/repo/a.go", s.Permission.Detail)
			},
		},
		{
			name: "stop failure is done and marked failed",
			from: working,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "StopFailure", "last_assistant_message": "API error"})
			},
			wantKind: KindDone,
			check:    func(t *testing.T, s Session) { assert.True(t, s.Failed) },
		},
		{
			name: "hooks after the end change nothing",
			from: ended,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "UserPromptSubmit"})
			},
			wantKind: KindEnded,
			check:    func(t *testing.T, s Session) { assert.Equal(t, ended, s) },
		},
		{
			name: "a resumed session starts again",
			from: ended,
			rec: func(t *testing.T) store.Record {
				return hookRecord(t, at, map[string]any{"hook_event_name": "SessionStart", "session_id": "s2"})
			},
			wantKind: KindIdle,
			check:    func(t *testing.T, s Session) { assert.Equal(t, "s2", s.ClaudeSessionID) },
		},
		{
			name:     "invalid line changes nothing",
			from:     question,
			rec:      func(*testing.T) store.Record { return store.Record{Source: store.SourceInvalid, Raw: "garbage"} },
			wantKind: KindQuestion,
			check:    func(t *testing.T, s Session) { assert.Equal(t, question, s) },
		},
		{
			name:     "non-JSON hook payload changes nothing",
			from:     question,
			rec:      func(*testing.T) store.Record { return store.Record{TS: at, Source: store.SourceHook, Raw: "oops"} },
			wantKind: KindQuestion,
			check:    func(t *testing.T, s Session) { assert.Equal(t, question, s) },
		},
		{
			name:     "unknown app event changes nothing",
			from:     question,
			rec:      func(*testing.T) store.Record { return appRecord(at, "something_new") },
			wantKind: KindQuestion,
			check:    func(t *testing.T, s Session) { assert.Equal(t, question, s) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Apply(tc.from, tc.rec(t))
			assert.Equal(t, tc.wantKind, s.Kind)
			if tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

func TestSeqAndSince(t *testing.T) {
	s := Session{Kind: KindWorking, Since: start, Seq: 1}
	later := start.Add(time.Minute)

	same := Apply(s, hookRecord(t, later, map[string]any{"hook_event_name": "UserPromptSubmit"}))
	assert.Equal(t, s.Seq, same.Seq, "queued prompt while working changes nothing visible")
	assert.Equal(t, start, same.Since)

	busy := Apply(s, hookRecord(t, later, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}))
	assert.Equal(t, s.Seq+1, busy.Seq, "new activity is visible")
	assert.Equal(t, start, busy.Since, "still working, so Since stays")
	assert.Equal(t, "Bash: go test ./...", busy.Activity)

	done := Apply(busy, hookRecord(t, later, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "ok"}))
	assert.Equal(t, busy.Seq+1, done.Seq)
	assert.Equal(t, later, done.Since)
}

func TestIsMainHook(t *testing.T) {
	assert.True(t, IsMainHook(hookRecord(t, start, map[string]any{"hook_event_name": "UserPromptSubmit"})))
	assert.False(t, IsMainHook(hookRecord(t, start, map[string]any{"hook_event_name": "Stop", "agent_id": "a1"})))
	assert.False(t, IsMainHook(hookRecord(t, start, map[string]any{"other": "field"})))
	assert.False(t, IsMainHook(appRecord(start, EventInterrupt)))
	assert.False(t, IsMainHook(store.Record{Source: store.SourceHook, Raw: "oops"}))
}

func TestViewFormatsTimeInUTC(t *testing.T) {
	kyiv := time.FixedZone("EEST", 3*60*60)
	s := Session{Kind: KindDone, Since: time.Date(2026, 10, 17, 13, 4, 5, 0, kyiv), Seq: 2, LastMessage: "ok"}

	v := s.View()

	assert.Equal(t, "2026-10-17 10:04:05", v.ChangedAt)
	assert.False(t, v.NeedsYou)
	assert.False(t, v.CanAnswer)
}

func appPayload(t *testing.T, at time.Time, event string, payload any) store.Record {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return store.Record{TS: at, Source: store.SourceApp, Event: event, Payload: body}
}

func TestScreenPrompt(t *testing.T) {
	at := start.Add(time.Minute)
	done := Session{Kind: KindDone, Since: start, Seq: 4, LastMessage: "All tests pass."}
	menu := Question{Question: "New MCP server found", Options: []Option{{Label: "Use it"}, {Label: "Skip it"}}}

	asked := Apply(done, appPayload(t, at, EventScreenPrompt, menu))
	require.Equal(t, KindQuestion, asked.Kind)
	assert.True(t, asked.FromScreen)
	assert.Equal(t, []Question{menu}, asked.Questions)
	assert.True(t, asked.View().CanAnswer)
	assert.Equal(t, done.Seq+1, asked.Seq)

	stale := Apply(asked, appPayload(t, at, EventScreenPromptGone, SeqPayload{Seq: done.Seq}))
	assert.Equal(t, asked, stale, "gone for an older state changes nothing")

	back := Apply(asked, appPayload(t, at, EventScreenPromptGone, SeqPayload{Seq: asked.Seq}))
	assert.Equal(t, KindDone, back.Kind)
	assert.False(t, back.FromScreen)
	assert.Equal(t, done.LastMessage, back.LastMessage, "the state before the menu comes back")
	assert.Equal(t, asked.Seq+1, back.Seq)

	hooked := Apply(asked, hookRecord(t, at, map[string]any{"hook_event_name": "UserPromptSubmit"}))
	assert.Equal(t, KindWorking, hooked.Kind)
	assert.False(t, hooked.FromScreen, "a hook takes over")
}

func TestScreenPromptNeverHidesAHookedQuestion(t *testing.T) {
	permission := Session{Kind: KindPermission, Since: start, Seq: 2, Permission: &Permission{Tool: "Bash", Detail: "date"}}
	menu := Question{Question: "Do you want to proceed?", Options: []Option{{Label: "Yes"}, {Label: "No"}}}

	s := Apply(permission, appPayload(t, start, EventScreenPrompt, menu))

	assert.Equal(t, permission, s)
}

func TestAnswered(t *testing.T) {
	at := start.Add(time.Minute)
	cases := []struct {
		name string
		from Session
		want Kind
	}{
		{name: "permission", from: Session{Kind: KindPermission, Seq: 3, Permission: &Permission{Tool: "Bash"}}, want: KindWorking},
		{name: "question", from: Session{Kind: KindQuestion, Seq: 3, Questions: []Question{{Question: "Which?"}}}, want: KindWorking},
		{name: "trust", from: Session{Kind: KindTrust, Seq: 3}, want: KindIdle},
		{name: "done stays done", from: Session{Kind: KindDone, Seq: 3}, want: KindDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Apply(tc.from, appPayload(t, at, EventAnswered, SeqPayload{Seq: tc.from.Seq}))
			assert.Equal(t, tc.want, s.Kind)
		})
	}

	next := Session{Kind: KindPermission, Seq: 7, Permission: &Permission{Tool: "Bash"}}
	assert.Equal(t, next, Apply(next, appPayload(t, at, EventAnswered, SeqPayload{Seq: 6})), "the next prompt stays")
}
