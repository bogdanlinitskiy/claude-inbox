package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
)

const command = "'/opt/bin/claude-inbox' hook --discover"

// existing is a settings file as a person keeps it: keys in their own order, a four-space indent, a key with
// <>& in it, another tool's hooks on one of the app's events and on an event the app does not use.
const existing = `{
    "model": "opus",
    "env": {"Z": "1", "A": "<b>&"},
    "hooks": {
        "PreToolUse": [
            {"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh", "timeout": 5}]}
        ],
        "Notification": [
            {"matcher": "*", "hooks": [{"type": "command", "command": "notify-send hi"}]}
        ]
    },
    "effortLevel": "high"
}
`

func keysOf(t *testing.T, data []byte) []string {
	t.Helper()
	members, err := parseObject(data)
	require.NoError(t, err)
	keys := make([]string, len(members))
	for i, m := range members {
		keys[i] = m.key
	}
	return keys
}

type file struct {
	Hooks map[string][]Matcher `json:"hooks"`
}

func decode(t *testing.T, data []byte) file {
	t.Helper()
	var f file
	require.NoError(t, json.Unmarshal(data, &f))
	return f
}

func TestInstallIntoAnEmptyFile(t *testing.T) {
	for _, data := range []string{"", "  \n", "{}\n"} {
		out, err := Install([]byte(data), command)

		require.NoError(t, err)
		f := decode(t, out)
		require.Len(t, f.Hooks, len(hook.Events))
		for _, event := range hook.Events {
			assert.Equal(t, []Matcher{{Matcher: matchAll, Hooks: []Hook{{Type: commandType, Command: command, Timeout: timeoutSeconds}}}}, f.Hooks[event], event)
		}
	}
}

func TestInstallKeepsTheRestOfTheFile(t *testing.T) {
	out, err := Install([]byte(existing), command)

	require.NoError(t, err)
	assert.Equal(t, keysOf(t, []byte(existing)), keysOf(t, out), "top-level keys keep their order")
	assert.Contains(t, string(out), `"A": "<b>&"`, "values keep their exact characters")
	assert.Contains(t, string(out), "\n    \"model\"", "the file's own indent")
	assert.True(t, strings.HasSuffix(string(out), "}\n"), "the final newline stays")

	members, err := parseObject(out)
	require.NoError(t, err)
	hookKeys := keysOf(t, lookup(members, hooksKey))
	assert.Equal(t, []string{"PreToolUse", "Notification"}, hookKeys[:2], "existing events first, in their order")
	assert.Len(t, hookKeys, len(hook.Events)+1)

	f := decode(t, out)
	require.Len(t, f.Hooks["PreToolUse"], 2)
	assert.Equal(t, "guard.sh", f.Hooks["PreToolUse"][0].Hooks[0].Command, "another tool's hook stays first")
	assert.Equal(t, command, f.Hooks["PreToolUse"][1].Hooks[0].Command)
	assert.Equal(t, "notify-send hi", f.Hooks["Notification"][0].Hooks[0].Command)
}

func TestInstallTwiceChangesNothing(t *testing.T) {
	once, err := Install([]byte(existing), command)
	require.NoError(t, err)

	twice, err := Install(once, command)

	require.NoError(t, err)
	assert.Equal(t, string(once), string(twice))
}

func TestInstallRecognisesAHandWrittenHook(t *testing.T) {
	data := []byte(`{"hooks": {"Stop": [{"matcher": "*", "hooks": [{"type": "command", "command": "~/.local/bin/claude-inbox hook --discover", "timeout": 5}]}]}}`)

	out, err := Install(data, command)

	require.NoError(t, err)
	f := decode(t, out)
	assert.Len(t, f.Hooks["Stop"], 1, "Stop has the hook already")
	assert.Len(t, f.Hooks["SessionStart"], 1)
}

func TestRemoveUndoesInstall(t *testing.T) {
	installed, err := Install([]byte(existing), command)
	require.NoError(t, err)

	out, err := Remove(installed)

	require.NoError(t, err)
	want, err := render(mustParse(t, []byte(existing)), []byte(existing))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(out), "only the app's entries go; events it added go with them")
}

func TestRemoveKeepsOtherHooksInTheSameEntry(t *testing.T) {
	data := []byte(`{"hooks": {"Stop": [{"matcher": "*", "hooks": [{"type": "command", "command": "say done"}, {"type": "command", "command": "claude-inbox hook --discover"}]}]}}`)

	out, err := Remove(data)

	require.NoError(t, err)
	f := decode(t, out)
	require.Len(t, f.Hooks["Stop"], 1)
	assert.Equal(t, []Hook{{Type: commandType, Command: "say done"}}, f.Hooks["Stop"][0].Hooks)
}

func TestRemoveWithoutTheHookChangesNothing(t *testing.T) {
	for _, data := range []string{"", existing, `{"model": "opus"}`} {
		out, err := Remove([]byte(data))

		require.NoError(t, err)
		assert.Equal(t, data, string(out))
	}
}

func TestRefusesFilesItCannotEditSafely(t *testing.T) {
	for name, data := range map[string]string{
		"top level is an array": `[]`,
		"hooks is a string":     `{"hooks": "x"}`,
		"event is an object":    `{"hooks": {"Stop": {}}}`,
		"broken JSON":           `{"hooks": `,
		"two documents":         `{} {}`,
	} {
		_, err := Install([]byte(data), command)
		assert.Error(t, err, name)
	}
}

func TestIsDiscoverHook(t *testing.T) {
	for command, want := range map[string]bool{
		"~/.local/bin/claude-inbox hook --discover":   true,
		"'/opt/my bin/claude-inbox' hook --discover":  true,
		`"/opt/my bin/claude-inbox" hook --discover`:  true,
		`/opt/my\ bin/claude-inbox hook --discover`:   true,
		DiscoverCommand("/opt/bin/claude-inbox"):      true,
		"claude-inbox hook --discover --state-dir /x": true,
		"claude-inbox hook --state-dir /x":            false,
		"other-tool hook --discover":                  false,
		"claude-inbox serve":                          false,
	} {
		assert.Equal(t, want, IsDiscoverHook(command), command)
	}
}

func TestDiscoverCommandQuotesThePath(t *testing.T) {
	path := "/opt/it's mine/claude-inbox"
	command := DiscoverCommand(path)

	assert.Equal(t, `'/opt/it'\''s mine/claude-inbox' hook --discover`, command)
	assert.Equal(t, []string{path, "hook", "--discover"}, shellWords(command), "the shell reads the path back whole")
}

func mustParse(t *testing.T, data []byte) []member {
	t.Helper()
	members, err := parseObject(data)
	require.NoError(t, err)
	return members
}
