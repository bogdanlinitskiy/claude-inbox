package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/store"
)

const (
	testID       = "abcd1234"
	testClaudeID = "62b5862e-495a-4bd4-b4c2-7ec0201ce108"
	testSocket   = "/tmp/tmux-1000/default"
	testPane     = "%12"
	testCwd      = "/home/user/work/acme-wallet"
)

var testClaude = proc.Process{PID: 4242, Start: 99}

func envWith(id string) func(string) string {
	return env(map[string]string{SessionEnv: id})
}

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func claudeFound() (proc.Process, bool) { return testClaude, true }

func claudeMissing() (proc.Process, bool) { return proc.Process{}, false }

func discoverPayload(t *testing.T, sessionID, cwd string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": sessionID, "cwd": cwd})
	require.NoError(t, err)
	return string(payload)
}

func TestRunStoresJSONPayloadOnOneLine(t *testing.T) {
	dir := store.New(t.TempDir())
	payload := "{\n  \"hook_event_name\": \"PermissionRequest\",\n  \"tool_name\": \"Bash\"\n}"
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	require.NoError(t, Run(dir, envWith(testID), strings.NewReader(payload), now))

	path, err := dir.EventsPath(testID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "\n"))

	records, _, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, store.SourceHook, records[0].Source)
	assert.JSONEq(t, payload, string(records[0].Payload))
	assert.True(t, now.Equal(records[0].TS))
}

func TestRunKeepsNonJSONAsRaw(t *testing.T) {
	dir := store.New(t.TempDir())
	body := "not json"

	require.NoError(t, Run(dir, envWith(testID), strings.NewReader(body), time.Now()))

	records, _, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Empty(t, records[0].Payload)
	assert.Equal(t, body, records[0].Raw)
}

func TestRunWithoutSessionRecordsNothing(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, Run(store.New(root), envWith(""), strings.NewReader(`{}`), time.Now()))

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRunRejectsInvalidSession(t *testing.T) {
	root := t.TempDir()

	err := Run(store.New(root), envWith("../../etc"), strings.NewReader(`{}`), time.Now())

	require.ErrorIs(t, err, store.ErrInvalidID)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestParallelRunsWriteWholeLines(t *testing.T) {
	dir := store.New(t.TempDir())
	const runs = 100
	// Large payloads: lines this long would interleave without the lock.
	filler := strings.Repeat("x", 64<<10)

	var wg sync.WaitGroup
	errs := make(chan error, runs)
	for i := range runs {
		wg.Go(func() {
			payload, err := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "n": i, "filler": filler})
			if err != nil {
				errs <- err
				return
			}
			errs <- Run(dir, envWith(testID), strings.NewReader(string(payload)), time.Now())
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	records, _, err := dir.ReadEvents(testID, 0)
	require.NoError(t, err)
	require.Len(t, records, runs)
	seen := make(map[int]bool, runs)
	for _, rec := range records {
		require.Equal(t, store.SourceHook, rec.Source, "line was not a whole record")
		var p struct {
			N int `json:"n"`
		}
		require.NoError(t, json.Unmarshal(rec.Payload, &p))
		seen[p.N] = true
	}
	assert.Len(t, seen, runs)
}

func TestDiscoverRecordsSessionUnderClaudeID(t *testing.T) {
	dir := store.New(t.TempDir())
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	payload := discoverPayload(t, testClaudeID, testCwd)
	vars := env(map[string]string{tmuxEnv: testSocket + ",1234,0", paneEnv: testPane})

	require.NoError(t, Discover(dir, vars, strings.NewReader(payload), now, claudeFound))

	meta, err := dir.LoadMeta(testClaudeID)
	require.NoError(t, err)
	assert.Equal(t, store.Meta{
		ID: testClaudeID, Repo: filepath.Base(testCwd), Dir: testCwd, CreatedAt: now, Discovered: true,
		PID: testClaude.PID, PIDStart: testClaude.Start, TmuxSocket: testSocket, TmuxPane: testPane,
	}, meta)
	records, _, err := dir.ReadEvents(testClaudeID, 0)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.JSONEq(t, payload, string(records[0].Payload))
}

func TestDiscoverWritesMetadataOnce(t *testing.T) {
	dir := store.New(t.TempDir())
	first := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	later := first.Add(time.Minute)

	require.NoError(t, Discover(dir, env(nil), strings.NewReader(discoverPayload(t, testClaudeID, testCwd)), first, claudeFound))
	require.NoError(t, Discover(dir, env(nil), strings.NewReader(discoverPayload(t, testClaudeID, "/elsewhere")), later, claudeMissing))

	meta, err := dir.LoadMeta(testClaudeID)
	require.NoError(t, err)
	assert.Equal(t, testCwd, meta.Dir)
	assert.True(t, first.Equal(meta.CreatedAt))
	records, _, err := dir.ReadEvents(testClaudeID, 0)
	require.NoError(t, err)
	assert.Len(t, records, 2)
}

func TestDiscoverWithoutTmuxOrClaudeProcess(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
	}{
		{name: "not in tmux", vars: nil},
		{name: "pane without socket", vars: map[string]string{paneEnv: testPane}},
		{name: "relative socket", vars: map[string]string{tmuxEnv: "default,1,0", paneEnv: testPane}},
		{name: "malformed pane", vars: map[string]string{tmuxEnv: testSocket + ",1,0", paneEnv: "12; rm -rf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := store.New(t.TempDir())

			require.NoError(t, Discover(dir, env(tc.vars), strings.NewReader(discoverPayload(t, testClaudeID, testCwd)), time.Now(), claudeMissing))

			meta, err := dir.LoadMeta(testClaudeID)
			require.NoError(t, err)
			assert.Empty(t, meta.TmuxSocket)
			assert.Empty(t, meta.TmuxPane)
			assert.Zero(t, meta.PID)
		})
	}
}

func TestDiscoverIgnoresRelativeCwd(t *testing.T) {
	dir := store.New(t.TempDir())

	require.NoError(t, Discover(dir, env(nil), strings.NewReader(discoverPayload(t, testClaudeID, "relative/dir")), time.Now(), claudeMissing))

	meta, err := dir.LoadMeta(testClaudeID)
	require.NoError(t, err)
	assert.Empty(t, meta.Dir)
	assert.Empty(t, meta.Repo)
}

func TestDiscoverSkipsAppSessions(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, Discover(store.New(root), envWith(testID), strings.NewReader(discoverPayload(t, testClaudeID, testCwd)), time.Now(), claudeFound))

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDiscoverRejectsInvalidSessionID(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{name: "path in the id", payload: discoverPayload(t, "../../etc/passwd", testCwd)},
		{name: "no id", payload: `{"hook_event_name":"Stop"}`},
		{name: "not json", payload: "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			err := Discover(store.New(root), env(nil), strings.NewReader(tc.payload), time.Now(), claudeFound)

			require.ErrorIs(t, err, store.ErrInvalidID)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}
