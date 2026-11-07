package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bogdanlinitskiy/claude-inbox/app/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/app/internal/state"
)

func TestTerminalRefusesUnknownAndEndedSessions(t *testing.T) {
	f := newFixture()

	missing := f.do(request(http.MethodGet, "/api/session/zzzz9999/terminal", ""))
	assert.Equal(t, http.StatusNotFound, missing.Code)

	info := f.sessions.infos[sessionID]
	info.State.Kind = state.KindEnded
	f.sessions.infos[sessionID] = info
	ended := f.do(request(http.MethodGet, "/api/session/"+sessionID+"/terminal", ""))
	assert.Equal(t, http.StatusConflict, ended.Code)
	assert.Equal(t, codeConflict, errorCode(t, ended))
}

func TestTerminalRefusesSessionsOutsideTmux(t *testing.T) {
	f := newFixture()
	f.sessions.attachErr = session.ErrNotInTmux

	rec := f.do(request(http.MethodGet, "/api/session/"+sessionID+"/terminal", ""))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, codeConflict, errorCode(t, rec))
}

func TestValidSide(t *testing.T) {
	assert.True(t, validSide(1))
	assert.True(t, validSide(maxSide))
	assert.False(t, validSide(0))
	assert.False(t, validSide(-5))
	assert.False(t, validSide(maxSide+1))
}

func TestInitialSize(t *testing.T) {
	cases := []struct {
		query      string
		cols, rows uint16
	}{
		{query: "?cols=130&rows=23", cols: 130, rows: 23},
		{query: "", cols: initialCols, rows: initialRows},
		{query: "?cols=130", cols: initialCols, rows: initialRows},
		{query: "?cols=abc&rows=23", cols: initialCols, rows: initialRows},
		{query: "?cols=0&rows=23", cols: initialCols, rows: initialRows},
		{query: "?cols=130&rows=99999", cols: initialCols, rows: initialRows},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/session/x/terminal"+tc.query, nil)
		cols, rows := initialSize(r)
		assert.Equal(t, tc.cols, cols, tc.query)
		assert.Equal(t, tc.rows, rows, tc.query)
	}
}

func TestTerminalEnvDropsOuterTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("TERM", "screen")

	env := terminalEnv()

	assert.Contains(t, env, "TERM="+terminalType)
	for _, kv := range env {
		assert.NotRegexp(t, `^TMUX(_PANE)?=`, kv)
		assert.NotEqual(t, "TERM=screen", kv)
	}
}
