package answer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

func TestParseMenuPermission(t *testing.T) {
	menu, ok := ParseMenu(screen(t, "permission-bash-manual.txt"))

	require.True(t, ok)
	assert.Equal(t, "Do you want to proceed?", menu.Question)
	require.Len(t, menu.Options, 4)
	assert.Equal(t, "Yes", menu.Options[0].Label)
	assert.Equal(t, "No", menu.Options[3].Label)
}

func TestParseMenuQuestionWithDescriptions(t *testing.T) {
	menu, ok := ParseMenu(screen(t, "question-2-options.txt"))

	require.True(t, ok)
	assert.Equal(t, "Which editor do you prefer?", menu.Question)
	assert.Equal(t, []state.Option{
		{Label: "vim", Description: "Modal terminal editor"},
		{Label: "nano", Description: "Simple terminal editor"},
		{Label: "Type something."},
		{Label: "Chat about this"},
	}, menu.Options)
}

func TestParseMenuFindsNoMenu(t *testing.T) {
	cases := map[string]string{
		"idle prompt after a reply":      screen(t, "after-deny-manual.txt"),
		"trust prompt has no numbers":    screen(t, "trust-prompt.txt"),
		"digit typed at the prompt":      "● Done.\n────\n❯ 1\n────\n",
		"numbered list in a reply":       "● Steps:\n  1. Build\n  2. Test\n────\n❯ \n",
		"a single entry":                 "Continue?\n❯ 1. Yes\nEsc to cancel\n",
		"past prompt as a numbered list": "❯ 1. Fix the parser\n  2. Add a test\n● Both done.\n────\n❯\u00a0\n────\n",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := ParseMenu(s)
			assert.False(t, ok)
		})
	}
}

// A prompt no hook reports, drawn the way Claude Code draws its menus.
func TestParseMenuWithoutHook(t *testing.T) {
	s := "● Done.\n\n New MCP server found in .mcp.json: jira\n\n ❯ 1. Use this and all future MCP servers in this project\n   2. Use this MCP server\n   3. Continue without using this MCP server\n\n Enter to confirm · Esc to cancel\n"

	menu, ok := ParseMenu(s)

	require.True(t, ok)
	assert.Equal(t, "New MCP server found in .mcp.json: jira", menu.Question)
	assert.Len(t, menu.Options, 3)
	assert.Equal(t, "Continue without using this MCP server", menu.Options[2].Label)
}

func TestMenuSame(t *testing.T) {
	a, ok := ParseMenu(screen(t, "permission-bash-manual.txt"))
	require.True(t, ok)
	b, ok := ParseMenu(screen(t, "question-2-options.txt"))
	require.True(t, ok)

	assert.True(t, a.Same(a))
	assert.False(t, a.Same(b))
}
