package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/server"
)

const (
	sendFrom  = "abcd1234"
	sendToken = "secret"
)

// sendServer stands in for serve: it records the request and answers with status and body.
func sendServer(t *testing.T, status int, body string) (*httptest.Server, *http.Request, map[string]string) {
	t.Helper()
	var got http.Request
	sent := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		w.WriteHeader(status)
		_, err := w.Write([]byte(body))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, sent
}

func stateWithLink(t *testing.T, link string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, urlFile), []byte(link), 0o600))
	return root
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestSend(t *testing.T) {
	srv, got, sent := sendServer(t, http.StatusOK, `{"tmuxName":"PROJ-1-wallet"}`)
	root := stateWithLink(t, srv.URL+"/?token="+sendToken)
	var out bytes.Buffer

	err := runSend([]string{"--state-dir", root, "PROJ-1-wallet", "Done:", "claim added."}, env(map[string]string{hook.SessionEnv: sendFrom}), srv.Client(), &out)

	require.NoError(t, err)
	assert.Equal(t, "/api/session/"+sendFrom+"/send", got.URL.Path)
	assert.Equal(t, srv.URL, got.Header.Get("Origin"))
	cookie, err := got.Cookie(server.TokenCookie)
	require.NoError(t, err)
	assert.Equal(t, sendToken, cookie.Value)
	assert.Equal(t, map[string]string{"to": "PROJ-1-wallet", "text": "Done: claim added."}, sent)
	assert.Equal(t, "Sent to PROJ-1-wallet.\n", out.String())
}

func TestSendReportsTheServersError(t *testing.T) {
	srv, _, _ := sendServer(t, http.StatusBadRequest, `{"message":"Validation error.","errors":{"to":"No session of that name in this group."},"code":"VALIDATION_CONSTRAINT"}`)
	root := stateWithLink(t, srv.URL+"/?token="+sendToken)

	err := runSend([]string{"--state-dir", root, "nobody", "hi"}, env(map[string]string{hook.SessionEnv: sendFrom}), srv.Client(), &bytes.Buffer{})

	require.Error(t, err)
	assert.Equal(t, "Validation error. No session of that name in this group.", err.Error())
}

func TestSendRefusals(t *testing.T) {
	root := t.TempDir()
	inSession := env(map[string]string{hook.SessionEnv: sendFrom})

	require.Error(t, runSend([]string{"--state-dir", root, "only-a-name"}, inSession, http.DefaultClient, &bytes.Buffer{}), "no text")
	require.ErrorContains(t, runSend([]string{"--state-dir", root, "x", "hi"}, env(nil), http.DefaultClient, &bytes.Buffer{}), hook.SessionEnv)
	require.ErrorContains(t, runSend([]string{"--state-dir", root, "x", "hi"}, inSession, http.DefaultClient, &bytes.Buffer{}), "not running")
}
