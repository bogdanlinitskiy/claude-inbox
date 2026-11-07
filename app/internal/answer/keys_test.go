package answer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/state"
)

const screensDir = "../../testdata/screens"

func screen(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(screensDir, name))
	require.NoError(t, err)
	return string(data)
}

func questionWith(options int) state.Session {
	q := state.Question{Question: "Which?"}
	for range options {
		q.Options = append(q.Options, state.Option{Label: "option"})
	}
	return state.Session{Kind: state.KindQuestion, Questions: []state.Question{q}}
}

func TestOption(t *testing.T) {
	cases := []struct {
		name    string
		options int
		pick    int
		want    string
	}{
		{name: "only option", options: 1, pick: 0, want: "1"},
		{name: "second of two", options: 2, pick: 1, want: "2"},
		{name: "third of three", options: 3, pick: 2, want: "3"},
		{name: "last of four", options: 4, pick: 3, want: "4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, err := Option(questionWith(tc.options), tc.pick)
			require.NoError(t, err)
			assert.Equal(t, []Step{{Keys: []string{tc.want}}}, steps)
		})
	}
}

func TestOptionOutOfRange(t *testing.T) {
	for _, pick := range []int{-1, 3} {
		_, err := Option(questionWith(3), pick)
		require.ErrorIs(t, err, ErrNoSuchOption, "pick %d", pick)
	}
}

func TestFreeTextUsesTypeSomething(t *testing.T) {
	cases := []struct {
		options int
		want    string
	}{
		{options: 1, want: "2"},
		{options: 2, want: "3"},
		{options: 3, want: "4"},
		{options: 4, want: "5"},
	}
	answerText := "mango, actually"
	for _, tc := range cases {
		steps, err := FreeText(questionWith(tc.options), answerText)
		require.NoError(t, err)
		assert.Equal(t, []Step{{Keys: []string{tc.want}}, {Text: answerText}, {Keys: []string{keyEnter}}}, steps, "%d options", tc.options)
	}
}

// A menu read from the screen has no "Type something" entry to type into.
func TestFreeTextRefusesAMenuFromTheScreen(t *testing.T) {
	s := questionWith(2)
	s.FromScreen = true

	_, err := FreeText(s, "anything")

	assert.ErrorIs(t, err, ErrNotAnswerable)
}

// The number in TestFreeTextUsesTypeSomething must match what Claude Code actually shows.
func TestTypeSomethingFollowsOptionsOnScreen(t *testing.T) {
	assert.Contains(t, screen(t, "question-2-options.txt"), "3. Type something.")
}

func TestQuestionsThatNeedTheTerminal(t *testing.T) {
	multiPart := state.Session{Kind: state.KindQuestion, Questions: []state.Question{{Question: "A?"}, {Question: "B?"}}}
	multiSelect := state.Session{Kind: state.KindQuestion, Questions: []state.Question{{Question: "A?", MultiSelect: true, Options: []state.Option{{Label: "x"}}}}}

	for name, s := range map[string]state.Session{"several parts": multiPart, "multi-select": multiSelect} {
		_, err := Option(s, 0)
		require.ErrorIs(t, err, ErrNotAnswerable, name)
		_, err = FreeText(s, "x")
		require.ErrorIs(t, err, ErrNotAnswerable, name)
	}
}

func TestTextChecks(t *testing.T) {
	q := questionWith(2)
	idle := state.Session{Kind: state.KindIdle}

	_, err := FreeText(q, "  ")
	require.ErrorIs(t, err, ErrEmptyText)
	_, err = FreeText(q, "line one\nline two")
	require.ErrorIs(t, err, ErrMultiline)
	_, err = Message(idle, "")
	require.ErrorIs(t, err, ErrEmptyText)
}

func TestMultilineMessageIsPasted(t *testing.T) {
	for _, prompt := range []string{"line one\nline two", "a\r\nb"} {
		steps, err := Message(state.Session{Kind: state.KindWorking}, prompt)
		require.NoError(t, err, prompt)
		assert.Equal(t, []Step{{Paste: prompt}, {Keys: []string{keyEnter}}}, steps, prompt)
	}
}

func TestPermissionAnswers(t *testing.T) {
	permission := state.Session{Kind: state.KindPermission, Permission: &state.Permission{Tool: "Bash", Detail: "date > deny-test.txt"}}

	steps, err := Allow(permission)
	require.NoError(t, err)
	assert.Equal(t, []Step{{Keys: []string{keyAllow}}}, steps)

	steps, err = Deny(permission, screen(t, "permission-bash-manual.txt"))
	require.NoError(t, err)
	assert.Equal(t, []Step{{Keys: []string{"4"}}}, steps)
}

func TestDenyWithoutMenuOnScreen(t *testing.T) {
	permission := state.Session{Kind: state.KindPermission}

	_, err := Deny(permission, screen(t, "after-deny-manual.txt"))

	require.ErrorIs(t, err, ErrNoMenuOnScreen)
}

func TestNoOptionNumber(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   int
	}{
		{
			name:   "manual-mode menu",
			screen: " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again\n   3. Yes, and switch to auto mode\n   4. No\n",
			want:   4,
		},
		{
			name:   "menu without the auto-mode entry",
			screen: " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again\n   3. No\n",
			want:   3,
		},
		{
			name:   "No with a follow-up text, selected",
			screen: " Do you want to proceed?\n   1. Yes\n ❯ 2. No, and tell Claude what to do differently\n",
			want:   2,
		},
		{
			name:   "edit menu",
			screen: " Do you want to make this edit to main.go?\n ❯ 1. Yes\n   2. Yes, allow all edits during this session (shift+tab)\n   3. No, and tell Claude what to do differently (esc)\n",
			want:   3,
		},
		{
			name:   "fetch menu",
			screen: " Do you want to allow Claude to fetch this content?\n ❯ 1. Yes\n   2. Yes, and don't ask again for example.com\n   3. No, and tell Claude what to do differently (esc)\n",
			want:   3,
		},
		{
			name:   "an older menu above does not count",
			screen: " Do you want to proceed?\n   9. No\n● done\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n",
			want:   2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NoOptionNumber(tc.screen)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNoOptionNumberIgnoresOtherNo(t *testing.T) {
	_, err := NoOptionNumber("❯ 1. Notes\n 2. Nothing\n")
	require.ErrorIs(t, err, ErrNoMenuOnScreen)

	_, err = NoOptionNumber(" Do you want to proceed?\n ❯ 1. Yes\n   2. Notebook\n")
	require.ErrorIs(t, err, ErrNoMenuOnScreen)
}

func TestTrust(t *testing.T) {
	trust := state.Session{Kind: state.KindTrust}

	steps, err := Trust(trust, true)
	require.NoError(t, err)
	assert.Equal(t, []Step{{Keys: []string{keyDown, keyEnter}}}, steps)

	steps, err = Trust(trust, false)
	require.NoError(t, err)
	assert.Equal(t, []Step{{Keys: []string{keyEnter}}}, steps)
}

func TestIsTrustPrompt(t *testing.T) {
	assert.True(t, IsTrustPrompt(screen(t, "trust-prompt.txt")))
	assert.False(t, IsTrustPrompt(screen(t, "permission-bash-manual.txt")))
	assert.False(t, IsTrustPrompt(screen(t, "question-2-options.txt")))
}

func TestMessageAndInterrupt(t *testing.T) {
	prompt := "Also put the word banana in your reply."
	for _, kind := range []state.Kind{state.KindIdle, state.KindDone} {
		steps, err := Message(state.Session{Kind: kind}, prompt)
		require.NoError(t, err, kind)
		assert.Equal(t, []Step{{Keys: []string{keyClearInput}}, {Text: prompt}, {Keys: []string{keyEnter}}}, steps, kind)
	}
	steps, err := Message(state.Session{Kind: state.KindWorking}, prompt)
	require.NoError(t, err)
	assert.Equal(t, []Step{{Text: prompt}, {Keys: []string{keyEnter}}}, steps, "a working session keeps its input box")

	steps, err = Interrupt(state.Session{Kind: state.KindWorking})
	require.NoError(t, err)
	assert.Equal(t, []Step{{Keys: []string{keyEscape}}}, steps)
}

func TestWrongState(t *testing.T) {
	working := state.Session{Kind: state.KindWorking}
	question := questionWith(2)

	calls := map[string]func() ([]Step, error){
		"option while working":    func() ([]Step, error) { return Option(working, 0) },
		"free text while working": func() ([]Step, error) { return FreeText(working, "x") },
		"allow on a question":     func() ([]Step, error) { return Allow(question) },
		"deny on a question":      func() ([]Step, error) { return Deny(question, "") },
		"message on a question":   func() ([]Step, error) { return Message(question, "x") },
		"interrupt when idle":     func() ([]Step, error) { return Interrupt(state.Session{Kind: state.KindIdle}) },
		"trust while working":     func() ([]Step, error) { return Trust(working, true) },
		"message after the end":   func() ([]Step, error) { return Message(state.Session{Kind: state.KindEnded}, "x") },
	}
	for name, call := range calls {
		_, err := call()
		require.ErrorIs(t, err, ErrWrongState, name)
	}
}
