package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/action"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const actionPath = "/api/session/" + sessionID + "/action"

func actionFixture(t *testing.T, kind state.Kind) *fixture {
	t.Helper()
	f := newFixture()
	f.srv.cfg.PrimaryActions = []action.Action{
		{Label: "Commit", Send: "Commit the changes"},
		{Label: "Ticket", Send: "Work on {input}", Input: "Ticket id"},
	}
	f.srv.cfg.SecondaryActions = []action.Action{{Label: "Echo", Run: `printf '%s' "$INBOX_INPUT"`, Input: "Text", Confirm: true}}
	info := f.sessions.infos[sessionID]
	info.Dir = t.TempDir()
	info.State.Kind = kind
	f.sessions.infos[sessionID] = info
	return f
}

func TestPageConfigListsActions(t *testing.T) {
	f := actionFixture(t, state.KindIdle)

	w := f.do(request(http.MethodGet, "/api/config", ""))

	var body pageConfig
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, []pageAction{
		{Label: "Commit", Kind: kindSend, Text: "Commit the changes"},
		{Label: "Ticket", Kind: kindSend, Text: "Work on {input}", Input: "Ticket id"},
	}, body.PrimaryActions)
	assert.Equal(t, []pageAction{{Label: "Echo", Kind: kindRun, Text: `printf '%s' "$INBOX_INPUT"`, Input: "Text", Confirm: true}}, body.SecondaryActions)
}

func TestSendActionTypesItsTextWithTheInput(t *testing.T) {
	f := actionFixture(t, state.KindIdle)
	ticket := "PROJ-7"

	w := f.do(request(http.MethodPost, actionPath, `{"list":"primary","index":1,"input":"`+ticket+`"}`))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"Work on " + ticket}, f.sessions.messages)
	var body actionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotNil(t, body.Session)
	assert.Equal(t, sessionID, body.Session.ID)
	assert.Nil(t, body.Run)
}

func TestRunActionRunsInTheSessionFolder(t *testing.T) {
	f := actionFixture(t, state.KindDone)
	input := "it's; rm -rf ~"
	req, err := json.Marshal(actionRequest{List: listSecondary, Index: 0, Input: input})
	require.NoError(t, err)

	w := f.do(request(http.MethodPost, actionPath, string(req)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body actionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotNil(t, body.Run)
	assert.Equal(t, action.Result{Output: input}, *body.Run, "the input arrives as data, never as shell")
	assert.Empty(t, f.sessions.messages)
}

func TestRunActionWaitsForTheTurnToEnd(t *testing.T) {
	f := actionFixture(t, state.KindWorking)

	w := f.do(request(http.MethodPost, actionPath, `{"list":"secondary","index":0,"input":"x"}`))

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, codeConflict, errorCode(t, w))
}

func TestRunActionWithoutAFolder(t *testing.T) {
	f := actionFixture(t, state.KindIdle)
	info := f.sessions.infos[sessionID]
	info.Dir = ""
	f.sessions.infos[sessionID] = info

	w := f.do(request(http.MethodPost, actionPath, `{"list":"secondary","index":0,"input":"x"}`))

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestActionValidation(t *testing.T) {
	cases := map[string]string{
		"unknown list":    `{"list":"tertiary","index":0}`,
		"index too large": `{"list":"primary","index":2}`,
		"negative index":  `{"list":"primary","index":-1}`,
		"missing input":   `{"list":"primary","index":1,"input":"  "}`,
		"unknown field":   `{"list":"primary","index":0,"text":"rm -rf ~"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := actionFixture(t, state.KindIdle)

			w := f.do(request(http.MethodPost, actionPath, body))

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, codeValidation, errorCode(t, w))
			assert.Empty(t, f.sessions.messages)
		})
	}
}

func TestActionOnAMissingSession(t *testing.T) {
	f := actionFixture(t, state.KindIdle)

	w := f.do(request(http.MethodPost, "/api/session/nope/action", `{"list":"secondary","index":0,"input":"x"}`))

	assert.Equal(t, http.StatusNotFound, w.Code)
}
