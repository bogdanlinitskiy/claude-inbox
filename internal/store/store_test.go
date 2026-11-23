package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testID       = "abcd1234"
	testClaudeID = "62b5862e-495a-4bd4-b4c2-7ec0201ce108"
)

func TestValidID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{name: "lowercase and digits", id: testID, want: true},
		{name: "claude session id", id: testClaudeID, want: true},
		{name: "uppercase claude session id", id: "62B5862E-495A-4BD4-B4C2-7EC0201CE108", want: false},
		{name: "claude session id with a path", id: "62b5862e-495a-4bd4-b4c2-7ec0201ce108/..", want: false},
		{name: "too short", id: "abc123", want: false},
		{name: "too long", id: "abcd12345", want: false},
		{name: "uppercase", id: "ABCD1234", want: false},
		{name: "path traversal", id: "../../x1", want: false},
		{name: "empty", id: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidID(tc.id))
		})
	}
}

func TestNewIDIsValid(t *testing.T) {
	for range 100 {
		id, err := NewID()
		require.NoError(t, err)
		require.True(t, ValidID(id), id)
	}
}

func TestEventsPathRejectsInvalidID(t *testing.T) {
	_, err := New(t.TempDir()).EventsPath("../evil")
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestAppendThenReadEvents(t *testing.T) {
	dir := New(t.TempDir())
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"hook_event_name":"Stop"}`)
	event := "interrupt"

	require.NoError(t, dir.Append(testID, Record{TS: now, Source: SourceHook, Payload: payload}))
	require.NoError(t, dir.AppendAppEvent(testID, event, now))

	records, offset, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, SourceHook, records[0].Source)
	assert.JSONEq(t, string(payload), string(records[0].Payload))
	assert.True(t, now.Equal(records[0].TS))
	assert.Equal(t, SourceApp, records[1].Source)
	assert.Equal(t, event, records[1].Event)

	path, err := dir.EventsPath(testID)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), offset)
	assert.Equal(t, os.FileMode(fileMode), info.Mode().Perm())
}

func TestReadEventsContinuesFromOffset(t *testing.T) {
	dir := New(t.TempDir())
	now := time.Now()
	first, second := "interrupt", "closed"
	require.NoError(t, dir.AppendAppEvent(testID, first, now))

	records, offset, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)

	require.NoError(t, dir.AppendAppEvent(testID, second, now))
	records, _, err = dir.ReadEvents(testID, offset)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, second, records[0].Event)
}

func TestReadEventsLeavesUnfinishedLine(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.AppendAppEvent(testID, "interrupt", time.Now()))
	path, err := dir.EventsPath(testID)
	require.NoError(t, err)
	appendRaw(t, path, `{"source":"app","event":"clo`)

	records, offset, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)

	appendRaw(t, path, "sed\"}\n")
	records, _, err = dir.ReadEvents(testID, offset)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "closed", records[0].Event)
}

func TestReadEventsMarksInvalidLines(t *testing.T) {
	dir := New(t.TempDir())
	path, err := dir.EventsPath(testID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), dirMode))
	garbage := "not a record"
	appendRaw(t, path, garbage+"\n")

	records, _, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, SourceInvalid, records[0].Source)
	assert.Equal(t, garbage, records[0].Raw)
}

func TestReadEventsWithoutFile(t *testing.T) {
	offset := int64(42)
	records, got, err := New(t.TempDir()).ReadEvents(testID, offset)
	require.NoError(t, err)
	assert.Empty(t, records)
	assert.Equal(t, offset, got)
}

func TestSaveAndLoadMetas(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	meta := Meta{
		ID:        testID,
		Ticket:    "PROJ-1600",
		Title:     "Contact form rate limit",
		Repo:      "acme-contact",
		Dir:       "/home/user/work/acme-contact",
		TmuxName:  "PROJ-1600-contact",
		CreatedAt: time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC),
	}

	require.NoError(t, dir.SaveMeta(meta))
	metas, err := dir.LoadMetas()
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, meta, metas[0])
}

func TestCreateMetaKeepsTheFirst(t *testing.T) {
	dir := New(t.TempDir())
	first := Meta{ID: testClaudeID, Dir: "/first", Discovered: true, PID: 42, PIDStart: 7, TmuxSocket: "/tmp/tmux-1000/default", TmuxPane: "%3"}
	second := Meta{ID: testClaudeID, Dir: "/second", Discovered: true}

	exists, err := dir.MetaExists(testClaudeID)
	require.NoError(t, err)
	assert.False(t, exists)
	require.NoError(t, dir.CreateMeta(first))
	require.NoError(t, dir.CreateMeta(second))

	exists, err = dir.MetaExists(testClaudeID)
	require.NoError(t, err)
	assert.True(t, exists)
	got, err := dir.LoadMeta(testClaudeID)
	require.NoError(t, err)
	assert.Equal(t, first, got)
	entries, err := os.ReadDir(filepath.Join(dir.Root(), sessionsDir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left behind")
}

func TestCreateMetaRejectsInvalidID(t *testing.T) {
	err := New(t.TempDir()).CreateMeta(Meta{ID: "../evil"})
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestIDsSkipsOtherFiles(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	require.NoError(t, dir.SaveMeta(Meta{ID: testID}))
	require.NoError(t, dir.CreateMeta(Meta{ID: testClaudeID}))
	for _, name := range []string{"notes.json", testID + ".json.tmp"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir.Root(), sessionsDir, name), []byte("{}"), fileMode))
	}

	ids, err := dir.IDs()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{testID, testClaudeID}, ids)
}

func TestArchive(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	require.NoError(t, dir.SaveMeta(Meta{ID: testID}))
	require.NoError(t, dir.AppendAppEvent(testID, "closed", time.Now()))

	require.NoError(t, dir.Archive(testID))

	metas, err := dir.LoadMetas()
	require.NoError(t, err)
	assert.Empty(t, metas)
	for _, name := range []string{testID + ".json", testID + ".jsonl"} {
		assert.FileExists(t, filepath.Join(dir.Root(), archiveDir, name))
	}
}

func TestArchivedMetas(t *testing.T) {
	d := New(t.TempDir())
	require.NoError(t, d.Init())
	meta := Meta{ID: "abcd1234", Title: "Rate limit", ClaudeID: "11111111-1111-4111-8111-111111111111"}
	require.NoError(t, d.SaveMeta(meta))
	require.NoError(t, d.Archive(meta.ID))

	metas, err := d.ArchivedMetas()

	require.NoError(t, err)
	assert.Equal(t, []Meta{meta}, metas)
}

func TestArchiveWithoutEvents(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	require.NoError(t, dir.SaveMeta(Meta{ID: testID}))

	require.NoError(t, dir.Archive(testID))
}

func TestPruneArchiveDeletesOnlyOldFiles(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	now := time.Now()
	old, recent := filepath.Join(dir.Root(), archiveDir, "old.jsonl"), filepath.Join(dir.Root(), archiveDir, "recent.jsonl")
	for _, path := range []string{old, recent} {
		require.NoError(t, os.WriteFile(path, nil, fileMode))
	}
	oldTime := now.Add(-archiveKeep - time.Hour)
	require.NoError(t, os.Chtimes(old, oldTime, oldTime))

	require.NoError(t, dir.PruneArchive(now))

	assert.NoFileExists(t, old)
	assert.FileExists(t, recent)
}

func TestInitIsRepeatable(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())
	require.NoError(t, dir.Init())
	for _, sub := range []string{sessionsDir, eventsDir, archiveDir} {
		info, err := os.Stat(filepath.Join(dir.Root(), sub))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(dirMode), info.Mode().Perm(), sub)
	}
}

func TestDefaultRootUsesXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	root, err := DefaultRoot()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, appDirName), root)
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	require.NoError(t, err)
	_, err = f.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestGroupMessages(t *testing.T) {
	dir := New(t.TempDir())
	require.NoError(t, dir.Init())

	none, err := dir.GroupMessages(testID)
	require.NoError(t, err)
	assert.Empty(t, none, "a group that sent nothing yet")

	first := GroupMessage{SentAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), From: "PROJ-1-coord", To: "PROJ-1-wallet", Text: "Add the claim."}
	second := GroupMessage{SentAt: first.SentAt.Add(time.Minute), From: "PROJ-1-wallet", To: "PROJ-1-coord", Text: "Done."}
	require.NoError(t, dir.AppendGroupMessage(testID, first))
	require.NoError(t, dir.AppendGroupMessage(testID, second))

	msgs, err := dir.GroupMessages(testID)
	require.NoError(t, err)
	assert.Equal(t, []GroupMessage{first, second}, msgs)
}

func TestGroupMessagesRejectInvalidID(t *testing.T) {
	dir := New(t.TempDir())
	_, err := dir.GroupMessages("../evil1")
	require.ErrorIs(t, err, ErrInvalidID)
	require.ErrorIs(t, dir.AppendGroupMessage("../evil1", GroupMessage{}), ErrInvalidID)
}
