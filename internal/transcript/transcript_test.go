package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	idOld     = "11111111-1111-4111-8111-111111111111"
	idNew     = "22222222-2222-4222-8222-222222222222"
	idEmpty   = "33333333-3333-4333-8333-333333333333"
	dir       = "/home/user/work/acme-wallet"
	branch    = "PROJ-1600-bonus"
	projectID = "-home-user-work-acme-wallet"
)

func jsonLine(t *testing.T, v map[string]any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data) + "\n"
}

func userLine(t *testing.T, content any, meta bool) string {
	return jsonLine(t, map[string]any{"type": "user", "cwd": dir, "gitBranch": branch, "isMeta": meta, "message": map[string]any{"role": "user", "content": content}})
}

// write saves a transcript; reply adds a line from Claude, without which it is no conversation.
func write(t *testing.T, root, folder, id string, modTime time.Time, lines ...string) {
	t.Helper()
	writeRaw(t, root, folder, id, modTime, append(lines, jsonLine(t, map[string]any{"type": "assistant", "cwd": dir, "message": map[string]any{"content": []map[string]any{{"type": "tool_use", "name": "Bash"}}}}))...)
}

func writeRaw(t *testing.T, root, folder, id string, modTime time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(root, folder, id+ext)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600))
	require.NoError(t, os.Chtimes(path, modTime, modTime))
}

func TestListReadsFolderBranchAndTitleLatestFirst(t *testing.T) {
	root := t.TempDir()
	older := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	aiTitle, customTitle := "Bonus rollback", "My name for it"
	write(t, root, projectID, idOld, older,
		jsonLine(t, map[string]any{"type": "file-history-snapshot"}),
		userLine(t, "fix the bonus", false),
		jsonLine(t, map[string]any{"type": "ai-title", "aiTitle": aiTitle}),
	)
	write(t, root, projectID, idNew, newer,
		userLine(t, "start", false),
		jsonLine(t, map[string]any{"type": "custom-title", "customTitle": customTitle}),
		jsonLine(t, map[string]any{"type": "ai-title", "aiTitle": aiTitle}),
	)

	list, err := List(root)

	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, Conversation{ID: idNew, Title: customTitle, Prompt: "start", Dir: dir, Branch: branch, UpdatedAt: newer}, normalize(list[0]))
	assert.Equal(t, Conversation{ID: idOld, Title: aiTitle, Prompt: "fix the bonus", Dir: dir, Branch: branch, UpdatedAt: older}, normalize(list[1]))
}

func normalize(c Conversation) Conversation {
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c
}

func TestListFallsBackToTheFirstTypedPrompt(t *testing.T) {
	root := t.TempDir()
	prompt := "Why does the  wallet\nlock up?"
	write(t, root, projectID, idOld, time.Now(),
		userLine(t, "<local-command-caveat>Caveat</local-command-caveat>", true),
		userLine(t, "<command-name>/clear</command-name>", false),
		userLine(t, []map[string]any{{"type": "tool_result", "content": "ok"}}, false),
		userLine(t, []map[string]any{{"type": "text", "text": prompt}}, false),
	)

	list, err := List(root)

	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, strings.Join(strings.Fields(prompt), " "), list[0].Title)
}

func TestListCutsLongPrompts(t *testing.T) {
	root := t.TempDir()
	write(t, root, projectID, idOld, time.Now(), userLine(t, strings.Repeat("ж", maxPromptRunes+10), false))

	list, err := List(root)

	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, strings.Repeat("ж", maxPromptRunes)+"…", list[0].Title)
}

// The title lines sit at the end of a long transcript, past everything the head window reads.
func TestListFindsTheTitleAtTheEndOfALongTranscript(t *testing.T) {
	root := t.TempDir()
	title := "Late title"
	filler := jsonLine(t, map[string]any{"type": "assistant", "cwd": dir, "message": map[string]any{"content": strings.Repeat("x", 1000)}})
	write(t, root, projectID, idOld, time.Now(),
		userLine(t, "go", false),
		strings.Repeat(filler, headBytes/len(filler)+tailBytes/len(filler)),
		jsonLine(t, map[string]any{"type": "ai-title", "aiTitle": title}),
	)

	list, err := List(root)

	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, title, list[0].Title)
	assert.Equal(t, dir, list[0].Dir)
}

func TestListSkipsWhatIsNoConversation(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, projectID, idEmpty, time.Now(), jsonLine(t, map[string]any{"type": "file-history-snapshot"}))
	writeRaw(t, root, projectID, idOld, time.Now(), userLine(t, "<command-name>/exit</command-name>", false), userLine(t, "<local-command-stdout>Bye!</local-command-stdout>", false))
	write(t, root, projectID, "not-a-uuid", time.Now(), userLine(t, "hi", false))
	write(t, root, filepath.Join(projectID, idNew, "subagents"), idNew, time.Now(), userLine(t, "hi", false))

	list, err := List(root)

	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	write(t, root, projectID, idOld, time.Now(), userLine(t, "hi", false))

	c, err := Find(root, idOld)
	require.NoError(t, err)
	assert.Equal(t, dir, c.Dir)

	for _, id := range []string{idNew, "../" + idOld, "*"} {
		_, err := Find(root, id)
		require.ErrorIs(t, err, ErrNotFound, id)
	}
}

func assistantLine(t *testing.T, blocks ...map[string]any) string {
	return jsonLine(t, map[string]any{"type": "assistant", "cwd": dir, "message": map[string]any{"role": "assistant", "content": blocks}})
}

func TestReadPreview(t *testing.T) {
	root := t.TempDir()
	reply := "All done.\nTests pass."
	write(t, root, projectID, idOld, time.Now(),
		userLine(t, "<local-command-caveat>Caveat</local-command-caveat>", true),
		userLine(t, "<command-message>spec</command-message>\n<command-name>/spec</command-name>\n<command-args>PROJ-1600</command-args>", false),
		assistantLine(t, map[string]any{"type": "text", "text": "Spec written."}),
		userLine(t, "second", false),
		userLine(t, []map[string]any{{"type": "tool_result", "content": "ok"}}, false),
		userLine(t, "third", false),
		userLine(t, "fourth", false),
		assistantLine(t, map[string]any{"type": "thinking", "thinking": "hm"}, map[string]any{"type": "text", "text": reply}),
		assistantLine(t, map[string]any{"type": "tool_use", "name": "Bash"}),
	)

	p, err := ReadPreview(root, idOld)

	require.NoError(t, err)
	assert.Equal(t, Preview{FirstPrompt: "/spec PROJ-1600", LastPrompts: []string{"second", "third", "fourth"}, LastReply: reply}, p)
}

func TestReadPreviewOfAnUnknownConversation(t *testing.T) {
	_, err := ReadPreview(t.TempDir(), idOld)

	require.ErrorIs(t, err, ErrNotFound)
}
