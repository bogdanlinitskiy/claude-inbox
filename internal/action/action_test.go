package action

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		action  Action
		wantErr bool
	}{
		{name: "send", action: Action{Label: "Commit", Send: "Commit the changes"}},
		{name: "send with input", action: Action{Label: "Ticket", Send: "Work on {input}", Input: "Ticket id"}},
		{name: "run", action: Action{Label: "Pull", Run: "git pull --ff-only", Confirm: true}},
		{name: "run with input", action: Action{Label: "Checkout", Run: `git switch "$INBOX_INPUT"`, Input: "Branch"}},
		{name: "no label", action: Action{Label: " ", Send: "x"}, wantErr: true},
		{name: "neither send nor run", action: Action{Label: "Nothing"}, wantErr: true},
		{name: "both send and run", action: Action{Label: "Both", Send: "x", Run: "y"}, wantErr: true},
		{name: "placeholder in run", action: Action{Label: "Checkout", Run: "git switch {input}", Input: "Branch"}, wantErr: true},
		{name: "input without placeholder", action: Action{Label: "Ticket", Send: "Work on it", Input: "Ticket id"}, wantErr: true},
		{name: "placeholder without input", action: Action{Label: "Ticket", Send: "Work on {input}"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.action.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestText(t *testing.T) {
	ticket := "PROJ-12"

	assert.Equal(t, "Work on "+ticket+", then review "+ticket, Action{Send: "Work on {input}, then review {input}"}.Text(ticket))
}

func TestRunInTheFolderWithTheInputInTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	input := `a "quoted"; echo injected`

	res, err := Run(context.Background(), dir, `pwd; printf '%s' "$INBOX_INPUT"`, input)

	require.NoError(t, err)
	assert.Equal(t, Result{Output: dir + "\n" + input}, res)
}

func TestRunReportsTheExitCodeAndStderr(t *testing.T) {
	res, err := Run(context.Background(), t.TempDir(), "echo broken >&2; exit 3", "")

	require.NoError(t, err)
	assert.Equal(t, Result{ExitCode: 3, Output: "broken\n"}, res)
}

func TestRunInAMissingFolder(t *testing.T) {
	_, err := Run(context.Background(), "/no/such/folder", "true", "")

	assert.Error(t, err)
}

func TestRunStopsTheWholeCommandWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()

	// sh starts sleep as a child; killing sh alone would leave sleep holding the output pipe.
	res, err := Run(ctx, t.TempDir(), "sleep 30; echo late", "")

	require.NoError(t, err)
	assert.Less(t, time.Since(started), 10*time.Second)
	assert.True(t, res.TimedOut)
	assert.NotZero(t, res.ExitCode)
	assert.NotContains(t, res.Output, "late")
}

func TestTailKeepsTheEnd(t *testing.T) {
	limit := 4
	out := &tail{limit: limit}

	_, err := out.Write([]byte("abc"))
	require.NoError(t, err)
	_, err = out.Write([]byte("defg"))
	require.NoError(t, err)

	assert.Equal(t, "defg", string(out.buf))
	assert.True(t, out.cut)
}
