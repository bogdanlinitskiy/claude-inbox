package tmux

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "ticket and repo", in: "PROJ-1128-wallet", want: true},
		{name: "with suffix", in: "PROJ-1128-wallet-2", want: true},
		{name: "underscore", in: "no_ticket-cms", want: true},
		{name: "dot", in: "PROJ-1.wallet", want: false},
		{name: "colon", in: "PROJ-1:wallet", want: false},
		{name: "space", in: "CLS 1", want: false},
		{name: "empty", in: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidName(tc.in))
		})
	}
}

func TestStartRejectsInvalidName(t *testing.T) {
	err := New("unused").Start(context.Background(), NewSession{Name: "a:b", Dir: t.TempDir(), Width: 80, Height: 24, Command: []string{"true"}})
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestAttachCommandUsesExactTarget(t *testing.T) {
	assert.Equal(t, []string{"tmux", "-S", "/tmp/tmux-1000/default", "attach-session", "-t", "%12"}, NewAt("/tmp/tmux-1000/default").AttachCommand("%12"))
	assert.Equal(t, []string{"tmux", "attach-session", "-t", "=PROJ-1-wallet"}, New("").AttachCommand("PROJ-1-wallet"))
	assert.Equal(t, []string{"tmux", "-L", "sock", "attach-session", "-t", "=PROJ-1-wallet"}, New("sock").AttachCommand("PROJ-1-wallet"))
}
