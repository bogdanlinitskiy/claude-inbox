package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const (
	commitPath = "/api/session/" + sessionID + "/commit"
	pushPath   = "/api/session/" + sessionID + "/push"
	prPath     = "/api/session/" + sessionID + "/pr"
)

func TestCommitRunsInTheSessionDir(t *testing.T) {
	f := newFixture()
	message := "PROJ-1 Stream the export"
	paths := []string{"src/Export.php", "tests/ExportTest.php"}
	body, err := json.Marshal(commitRequest{Message: "  " + message + "\n", Paths: paths})
	require.NoError(t, err)

	w := f.do(request(http.MethodPost, commitPath, string(body)))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, f.sessions.infos[sessionID].Dir, f.gitDir)
	assert.Equal(t, message, f.commitMessage)
	assert.Equal(t, paths, f.commitPaths)
	var res gitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Contains(t, res.Output, message)
}

func TestCommitValidation(t *testing.T) {
	f := newFixture()

	noMessage := f.do(request(http.MethodPost, commitPath, `{"message":" ","paths":["a.go"]}`))
	noPaths := f.do(request(http.MethodPost, commitPath, `{"message":"work","paths":[]}`))

	assert.Equal(t, http.StatusBadRequest, noMessage.Code)
	assert.Equal(t, http.StatusBadRequest, noPaths.Code)
	assert.Empty(t, f.gitDir, "nothing ran")
}

func TestCommitWaitsForTheTurnToEnd(t *testing.T) {
	f := newFixture()
	info := f.sessions.infos[sessionID]
	info.State.Kind = state.KindWorking
	f.sessions.infos[sessionID] = info

	commit := f.do(request(http.MethodPost, commitPath, `{"message":"work","paths":["a.go"]}`))
	push := f.do(request(http.MethodPost, pushPath, ""))

	assert.Equal(t, http.StatusConflict, commit.Code)
	assert.Equal(t, http.StatusOK, push.Code, "a push only sends what is committed")
}

func TestGitFailureIsAConflictWithItsOutput(t *testing.T) {
	f := newFixture()
	why := "git push: exit status 1: ! [rejected] PROJ-1 -> PROJ-1 (fetch first)"
	f.gitErr = errors.New(why)

	w := f.do(request(http.MethodPost, pushPath, ""))

	assert.Equal(t, http.StatusConflict, w.Code)
	var body errorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, why, body.Message)
}

func TestOpenPRAnswersTheURL(t *testing.T) {
	f := newFixture()

	w := f.do(request(http.MethodPost, prPath, ""))

	require.Equal(t, http.StatusOK, w.Code)
	var res gitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Equal(t, prURL, res.URL)
	assert.Equal(t, f.sessions.infos[sessionID].Dir, f.gitDir)
}

func TestOpenPRWithoutGh(t *testing.T) {
	f := newFixture()
	f.srv.cfg.OpenPR = nil

	w := f.do(request(http.MethodPost, prPath, ""))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGitWithoutAFolder(t *testing.T) {
	f := newFixture()
	info := f.sessions.infos[sessionID]
	info.Dir = ""
	f.sessions.infos[sessionID] = info

	for _, path := range []string{pushPath, prPath} {
		w := f.do(request(http.MethodPost, path, ""))
		assert.Equal(t, http.StatusConflict, w.Code, path)
	}
	assert.Empty(t, f.gitDir, "no git command may run in the server's own folder")
}
