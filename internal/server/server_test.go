package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/transcript"
)

const (
	testPort  = 7788
	testToken = "secret-token"
	host      = "127.0.0.1:7788"
	origin    = "http://127.0.0.1:7788"
	sessionID = "abcd1234"
	groupID   = "grp12345"
	pageText  = "the page"
)

type fakeSessions struct {
	infos     map[string]session.Info
	started   []session.StartRequest
	groups    []session.StartGroupRequest
	// sends are the claude-inbox send calls as "from to text"; groupMsgs is what GroupMessages returns.
	sends     []string
	sendErr   error
	groupMsgs []store.GroupMessage
	answers   []session.AnswerRequest
	messages  []string
	closed    []string
	answerErr error
	startErr  error
	attach    []string
	attachErr error
	// conversations is what History lists; resumed are the conversations asked to resume.
	conversations []session.Conversation
	resumed       []string
	preview       transcript.Preview
	changes       chan session.Change
	subscribed    chan struct{}
}

func newFakeSessions() *fakeSessions {
	info := session.Info{ID: sessionID, Ticket: "PROJ-1", Repo: "acme-contact", Dir: "/work/acme-contact", State: state.View{Kind: state.KindQuestion, Seq: 4}}
	return &fakeSessions{
		infos:      map[string]session.Info{sessionID: info},
		changes:    make(chan session.Change, 4),
		subscribed: make(chan struct{}, 1),
	}
}

func (f *fakeSessions) List() []session.Info {
	return []session.Info{f.infos[sessionID]}
}

func (f *fakeSessions) Get(id string) (session.Info, error) {
	info, ok := f.infos[id]
	if !ok {
		return session.Info{}, session.ErrNotFound
	}
	return info, nil
}

func (f *fakeSessions) Start(_ context.Context, req session.StartRequest) (session.Info, error) {
	f.started = append(f.started, req)
	return f.infos[sessionID], f.startErr
}

func (f *fakeSessions) StartGroup(_ context.Context, req session.StartGroupRequest) ([]session.Info, error) {
	f.groups = append(f.groups, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return []session.Info{f.infos[sessionID]}, nil
}

func (f *fakeSessions) Send(_ context.Context, fromID, to, text string) (session.Info, error) {
	f.sends = append(f.sends, fromID+" "+to+" "+text)
	if f.sendErr != nil {
		return session.Info{}, f.sendErr
	}
	return f.Get(sessionID)
}

func (f *fakeSessions) GroupMessages(group string) ([]store.GroupMessage, error) {
	if group != groupID {
		return nil, session.ErrNotFound
	}
	return f.groupMsgs, nil
}

func (f *fakeSessions) Close(_ context.Context, id string) error {
	if _, err := f.Get(id); err != nil {
		return err
	}
	f.closed = append(f.closed, id)
	return nil
}

func (f *fakeSessions) Answer(_ context.Context, id string, a session.AnswerRequest) (session.Info, error) {
	f.answers = append(f.answers, a)
	if f.answerErr != nil {
		return session.Info{}, f.answerErr
	}
	return f.Get(id)
}

func (f *fakeSessions) Message(_ context.Context, id, prompt string) (session.Info, error) {
	f.messages = append(f.messages, prompt)
	return f.Get(id)
}

func (f *fakeSessions) Interrupt(_ context.Context, id string) (session.Info, error) {
	return f.Get(id)
}

func (f *fakeSessions) Conversations() ([]session.Conversation, error) {
	return f.conversations, nil
}

func (f *fakeSessions) ResumeConversation(_ context.Context, claudeSessionID string) (session.Info, error) {
	f.resumed = append(f.resumed, claudeSessionID)
	return f.Get(sessionID)
}

func (f *fakeSessions) Preview(claudeSessionID string) (transcript.Preview, error) {
	for _, c := range f.conversations {
		if c.ID == claudeSessionID {
			return f.preview, nil
		}
	}
	return transcript.Preview{}, session.ErrNotFound
}

func (f *fakeSessions) Rename(id, title string) (session.Info, error) {
	info, err := f.Get(id)
	if err != nil {
		return session.Info{}, err
	}
	info.Title = title
	f.infos[id] = info
	return info, nil
}

func (f *fakeSessions) Pin(id string, pinned bool) (session.Info, error) {
	info, err := f.Get(id)
	if err != nil {
		return session.Info{}, err
	}
	info.Pinned = pinned
	f.infos[id] = info
	return info, nil
}

func (f *fakeSessions) Repos() ([]session.Repo, error) {
	return []session.Repo{{Name: "acme-contact", Dir: "/work/acme-contact"}}, nil
}

func (f *fakeSessions) Attach(id string) ([]string, error) {
	if _, err := f.Get(id); err != nil {
		return nil, err
	}
	return f.attach, f.attachErr
}

func (f *fakeSessions) Subscribe() (<-chan session.Change, func()) {
	f.subscribed <- struct{}{}
	return f.changes, func() {}
}

type fixture struct {
	srv      *Server
	sessions *fakeSessions
	diffDir  string
	diffErr  error
	ideDir   string
	// What the Diff tab's git buttons were called with, and what they fail with.
	gitDir        string
	commitMessage string
	commitPaths   []string
	gitErr        error
}

const prURL = "https://github.com/acme/contact/pull/7"

func newFixture() *fixture {
	f := &fixture{sessions: newFakeSessions()}
	f.srv = New(Config{
		Port: testPort, Token: testToken, Sessions: f.sessions,
		Diff: func(_ context.Context, dir string) (gitdiff.Diff, error) {
			f.diffDir = dir
			return gitdiff.Diff{Base: "master"}, f.diffErr
		},
		OpenIDE: func(_ context.Context, dir string) error {
			f.ideDir = dir
			return nil
		},
		Commit: func(_ context.Context, dir, message string, paths []string) (string, error) {
			f.gitDir, f.commitMessage, f.commitPaths = dir, message, paths
			return "[PROJ-1 abc1234] " + message, f.gitErr
		},
		Push: func(_ context.Context, dir string) (string, error) {
			f.gitDir = dir
			return "To github.com:acme/contact.git", f.gitErr
		},
		OpenPR: func(_ context.Context, dir string) (string, error) {
			f.gitDir = dir
			return prURL, f.gitErr
		},
		Web:    fstest.MapFS{"index.html": {Data: []byte(pageText)}, "app.js": {Data: []byte("js")}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return f
}

// request builds a request as the page sends it: right host, cookie, and Origin on anything that changes state.
func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: testToken})
	if method != http.MethodGet {
		r.Header.Set("Origin", origin)
	}
	return r
}

func (f *fixture) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	assert.NotEmpty(t, body.Message)
	return body.Code
}

func TestGuard(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(r *http.Request)
		method   string
		wantCode int
		wantBody string
	}{
		{name: "another host name", method: http.MethodGet, prepare: func(r *http.Request) { r.Host = "evil.example:7788" }, wantCode: http.StatusForbidden, wantBody: codeForbidden},
		{name: "no cookie", method: http.MethodGet, prepare: func(r *http.Request) { r.Header.Del("Cookie") }, wantCode: http.StatusUnauthorized, wantBody: codeAccessDenied},
		{name: "wrong cookie", method: http.MethodGet, prepare: func(r *http.Request) {
			r.Header.Del("Cookie")
			r.AddCookie(&http.Cookie{Name: TokenCookie, Value: "guess"})
		}, wantCode: http.StatusUnauthorized, wantBody: codeAccessDenied},
		{name: "post without origin", method: http.MethodPost, prepare: func(r *http.Request) { r.Header.Del("Origin") }, wantCode: http.StatusForbidden, wantBody: codeForbidden},
		{name: "post from another site", method: http.MethodPost, prepare: func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, wantCode: http.StatusForbidden, wantBody: codeForbidden},
		{name: "socket upgrade from another site", method: http.MethodGet, prepare: func(r *http.Request) {
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Origin", "https://evil.example")
		}, wantCode: http.StatusForbidden, wantBody: codeForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			path := "/api/sessions"
			if tc.method == http.MethodPost {
				path = "/api/session/" + sessionID + "/interrupt"
			}
			r := request(tc.method, path, "")
			tc.prepare(r)

			w := f.do(r)

			assert.Equal(t, tc.wantCode, w.Code)
			assert.Equal(t, tc.wantBody, errorCode(t, w))
		})
	}
}

func TestLocalhostIsAllowedToo(t *testing.T) {
	f := newFixture()
	r := request(http.MethodPost, "/api/session/"+sessionID+"/interrupt", "")
	r.Host = "localhost:7788"
	r.Header.Set("Origin", "http://localhost:7788")

	assert.Equal(t, http.StatusOK, f.do(r).Code)
}

func TestTokenLinkSetsCookie(t *testing.T) {
	f := newFixture()
	r := httptest.NewRequest(http.MethodGet, "/?token="+testToken, http.NoBody)
	r.Host = host

	w := f.do(r)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, testToken, cookies[0].Value)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
}

func TestOldTokenLinkIsRefused(t *testing.T) {
	f := newFixture()
	r := httptest.NewRequest(http.MethodGet, "/?token=from-last-run", http.NoBody)
	r.Host = host

	w := f.do(r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Result().Cookies())
}

func TestPage(t *testing.T) {
	f := newFixture()

	page := f.do(request(http.MethodGet, "/", ""))
	asset := f.do(request(http.MethodGet, "/app.js", ""))
	noCookie := httptest.NewRequest(http.MethodGet, "/app.js", http.NoBody)
	noCookie.Host = host

	assert.Equal(t, http.StatusOK, page.Code)
	assert.Equal(t, pageText, page.Body.String())
	assert.Equal(t, http.StatusOK, asset.Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(noCookie).Code)
}

func TestFrameAncestors(t *testing.T) {
	embedOrigin := "http://127.0.0.1:9000"
	own := newFixture()
	embedded := newFixture()
	embedded.srv = New(Config{Port: testPort, Token: testToken, Sessions: embedded.sessions, EmbedOrigin: embedOrigin, Logger: own.srv.cfg.Logger})
	foreignHost := request(http.MethodGet, "/api/sessions", "")
	foreignHost.Host = "evil.example:7788"

	assert.Equal(t, "frame-ancestors 'self'", own.do(request(http.MethodGet, "/api/sessions", "")).Header().Get("Content-Security-Policy"))
	assert.Equal(t, "frame-ancestors 'self'", own.do(foreignHost).Header().Get("Content-Security-Policy"), "refused requests carry it too")
	assert.Equal(t, "frame-ancestors 'self' "+embedOrigin, embedded.do(request(http.MethodGet, "/api/sessions", "")).Header().Get("Content-Security-Policy"))
}

func TestCheatSheet(t *testing.T) {
	f := newFixture()
	target := "http://127.0.0.1:9000/cheat.html"

	assert.Equal(t, http.StatusNotFound, f.do(request(http.MethodGet, "/cheatsheet", "")).Code, "off by default")

	f.srv.cfg.CheatSheet = target
	w := f.do(request(http.MethodGet, "/cheatsheet", ""))
	noCookie := httptest.NewRequest(http.MethodGet, "/cheatsheet", http.NoBody)
	noCookie.Host = host

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, target, w.Header().Get("Location"))
	assert.Equal(t, http.StatusUnauthorized, f.do(noCookie).Code)
}

func TestListAndGet(t *testing.T) {
	f := newFixture()

	list := f.do(request(http.MethodGet, "/api/sessions", ""))
	require.Equal(t, http.StatusOK, list.Code)
	var infos []session.Info
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &infos))
	require.Len(t, infos, 1)
	assert.Equal(t, sessionID, infos[0].ID)

	missing := f.do(request(http.MethodGet, "/api/session/zzzz9999", ""))
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Equal(t, codeNotFound, errorCode(t, missing))
}

func TestStart(t *testing.T) {
	f := newFixture()
	body := `{"dir":"/work/acme-contact","ticket":"PROJ-1600","title":"Rate limit","prompt":"/spec PROJ-1600"}`

	w := f.do(request(http.MethodPost, "/api/sessions", body))

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, []session.StartRequest{{Dir: "/work/acme-contact", Ticket: "PROJ-1600", Title: "Rate limit", Prompt: "/spec PROJ-1600"}}, f.sessions.started)
}

func TestStartInvalidRepo(t *testing.T) {
	f := newFixture()
	f.sessions.startErr = session.ErrInvalidRepo

	w := f.do(request(http.MethodPost, "/api/sessions", `{"dir":"/etc"}`))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body errorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, codeValidation, body.Code)
	assert.Contains(t, body.Errors, "dir")
}

func TestStartGroup(t *testing.T) {
	f := newFixture()
	body := `{"dirs":["/work/acme-contact","/work/acme-wallet"],"ticket":"PROJ-1600","prompt":"Split it","worktree":true,"branch":"PROJ-1600-x","coordinator":true,"shareDirs":true}`

	w := f.do(request(http.MethodPost, "/api/groups", body))

	assert.Equal(t, http.StatusCreated, w.Code)
	want := session.StartGroupRequest{
		Dirs: []string{"/work/acme-contact", "/work/acme-wallet"}, Ticket: "PROJ-1600", Prompt: "Split it",
		Worktree: true, Branch: "PROJ-1600-x", Coordinator: true, ShareDirs: true,
	}
	assert.Equal(t, []session.StartGroupRequest{want}, f.sessions.groups)
	var infos []session.Info
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &infos))
	assert.Len(t, infos, 1)
}

func TestStartGroupWithoutRepositories(t *testing.T) {
	f := newFixture()
	f.sessions.startErr = session.ErrNoRepos

	w := f.do(request(http.MethodPost, "/api/groups", `{"dirs":[]}`))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body errorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body.Errors, "dirs")
}

func TestSend(t *testing.T) {
	f := newFixture()
	to, text := "PROJ-1-wallet", "Done: claim added."

	w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/send", `{"to":"`+to+`","text":"`+text+`"}`))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{sessionID + " " + to + " " + text}, f.sessions.sends)
}

func TestSendErrors(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{err: session.ErrNoGroup, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: session.ErrNotInGroup, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: session.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: codeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			f := newFixture()
			f.sessions.sendErr = tc.err

			w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/send", `{"to":"x","text":"hi"}`))

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantCode, errorCode(t, w))
		})
	}
}

func TestGroupMessages(t *testing.T) {
	f := newFixture()
	f.sessions.groupMsgs = []store.GroupMessage{{From: "PROJ-1-coord", To: "PROJ-1-wallet", Text: "Add the claim."}}

	w := f.do(request(http.MethodGet, "/api/group/"+groupID+"/messages", ""))

	assert.Equal(t, http.StatusOK, w.Code)
	var msgs []store.GroupMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &msgs))
	assert.Equal(t, f.sessions.groupMsgs, msgs)
	assert.Equal(t, http.StatusNotFound, f.do(request(http.MethodGet, "/api/group/unknown1/messages", "")).Code)
}

func TestBodyValidation(t *testing.T) {
	f := newFixture()
	for _, body := range []string{`not json`, `{"seq":1,"surprise":true}`} {
		w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/answer", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
		assert.Equal(t, codeValidation, errorCode(t, w), body)
	}
	assert.Empty(t, f.sessions.answers)
}

func TestAnswer(t *testing.T) {
	f := newFixture()

	w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/answer", `{"seq":4,"option":1}`))

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, f.sessions.answers, 1)
	assert.Equal(t, 4, f.sessions.answers[0].Seq)
	require.NotNil(t, f.sessions.answers[0].Option)
	assert.Equal(t, 1, *f.sessions.answers[0].Option)
}

func TestAnswerErrors(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{err: session.ErrStale, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: answer.ErrWrongState, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: answer.ErrNotAnswerable, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: answer.ErrNoMenuOnScreen, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: answer.ErrMultiline, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: answer.ErrEmptyText, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: answer.ErrNoSuchOption, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: session.ErrInvalidAnswer, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: session.ErrNotInTmux, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: session.ErrStillRunning, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: session.ErrNoFolder, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: session.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: codeNotFound},
		{err: session.ErrNothingOnScreen, wantStatus: http.StatusConflict, wantCode: codeNothingOnScreen},
		{err: session.ErrAlreadySent, wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: fmt.Errorf("%w: git worktree: fatal: already checked out", session.ErrWorktree), wantStatus: http.StatusConflict, wantCode: codeConflict},
		{err: session.ErrNoBranch, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: gitdiff.ErrBadBranch, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: session.ErrNoWorktree, wantStatus: http.StatusBadRequest, wantCode: codeValidation},
		{err: io.ErrUnexpectedEOF, wantStatus: http.StatusInternalServerError, wantCode: codeServerError},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			f := newFixture()
			f.sessions.answerErr = tc.err

			w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/answer", `{"seq":4,"option":0}`))

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantCode, errorCode(t, w))
		})
	}
}

func TestMessageInterruptClose(t *testing.T) {
	f := newFixture()
	prompt := "Also say banana."

	assert.Equal(t, http.StatusOK, f.do(request(http.MethodPost, "/api/session/"+sessionID+"/message", `{"text":"`+prompt+`"}`)).Code)
	assert.Equal(t, []string{prompt}, f.sessions.messages)
	assert.Equal(t, http.StatusOK, f.do(request(http.MethodPost, "/api/session/"+sessionID+"/interrupt", "")).Code)
	assert.Equal(t, http.StatusNoContent, f.do(request(http.MethodDelete, "/api/session/"+sessionID, "")).Code)
	assert.Equal(t, []string{sessionID}, f.sessions.closed)
}

func TestConversations(t *testing.T) {
	f := newFixture()
	conversation := session.Conversation{ID: "62b5862e-495a-4bd4-b4c2-7ec0201ce108", Title: "Bonus rollback", Repo: "acme-wallet"}
	f.sessions.conversations = []session.Conversation{conversation}

	w := f.do(request(http.MethodGet, "/api/conversations", ""))

	require.Equal(t, http.StatusOK, w.Code)
	var got []session.Conversation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, f.sessions.conversations, got)

	f.sessions.preview = transcript.Preview{FirstPrompt: "/spec PROJ-1600", LastPrompts: []string{"go on"}, LastReply: "Done."}
	w = f.do(request(http.MethodGet, "/api/conversation/"+conversation.ID+"/preview", ""))
	require.Equal(t, http.StatusOK, w.Code)
	var preview transcript.Preview
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	assert.Equal(t, f.sessions.preview, preview)
	assert.Equal(t, http.StatusNotFound, f.do(request(http.MethodGet, "/api/conversation/unknown/preview", "")).Code)

	w = f.do(request(http.MethodPost, "/api/conversation/"+conversation.ID+"/resume", ""))

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, []string{conversation.ID}, f.sessions.resumed)
}

func TestRename(t *testing.T) {
	f := newFixture()
	title := "Rate limit"

	w := f.do(request(http.MethodPatch, "/api/session/"+sessionID, `{"title":"`+title+`"}`))

	require.Equal(t, http.StatusOK, w.Code)
	var info session.Info
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, sessionID, info.ID)
	assert.Equal(t, title, info.Title)
}

func TestRenameErrors(t *testing.T) {
	f := newFixture()

	missing := f.do(request(http.MethodPatch, "/api/session/zzzz9999", `{"title":"x"}`))
	unknownField := f.do(request(http.MethodPatch, "/api/session/"+sessionID, `{"ticket":"PROJ-2"}`))

	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Equal(t, http.StatusBadRequest, unknownField.Code)
	assert.Equal(t, codeValidation, errorCode(t, unknownField))
}

func TestPin(t *testing.T) {
	f := newFixture()
	path := "/api/session/" + sessionID + "/pin"

	pin := f.do(request(http.MethodPut, path, ""))
	pinned := f.sessions.infos[sessionID].Pinned
	unpin := f.do(request(http.MethodDelete, path, ""))

	require.Equal(t, http.StatusOK, pin.Code)
	assert.True(t, pinned)
	require.Equal(t, http.StatusOK, unpin.Code)
	assert.False(t, f.sessions.infos[sessionID].Pinned)
	var info session.Info
	require.NoError(t, json.Unmarshal(unpin.Body.Bytes(), &info))
	assert.Equal(t, sessionID, info.ID)
}

func TestPinUnknownSession(t *testing.T) {
	f := newFixture()

	w := f.do(request(http.MethodPut, "/api/session/zzzz9999/pin", ""))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiffAndOpenIDEUseTheSessionDir(t *testing.T) {
	f := newFixture()
	dir := f.sessions.infos[sessionID].Dir

	diff := f.do(request(http.MethodGet, "/api/session/"+sessionID+"/diff", ""))
	ide := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/open-ide", ""))

	assert.Equal(t, http.StatusOK, diff.Code)
	assert.Equal(t, dir, f.diffDir)
	assert.Equal(t, http.StatusNoContent, ide.Code)
	assert.Equal(t, dir, f.ideDir)
}

func TestDiffAndOpenIDEWithoutAFolder(t *testing.T) {
	f := newFixture()
	info := f.sessions.infos[sessionID]
	info.Dir = ""
	f.sessions.infos[sessionID] = info

	diff := f.do(request(http.MethodGet, "/api/session/"+sessionID+"/diff", ""))
	ide := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/open-ide", ""))

	assert.Equal(t, http.StatusConflict, diff.Code)
	assert.Equal(t, http.StatusConflict, ide.Code)
	assert.Empty(t, f.diffDir, "no git command may run in the server's own folder")
	assert.Empty(t, f.ideDir)
}

func TestOpenIDEWithoutAnIDE(t *testing.T) {
	f := newFixture()
	f.srv.cfg.OpenIDE = nil

	w := f.do(request(http.MethodPost, "/api/session/"+sessionID+"/open-ide", ""))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, codeNotFound, errorCode(t, w))
}

func TestPageConfig(t *testing.T) {
	f := newFixture()

	withIDE := f.do(request(http.MethodGet, "/api/config", ""))
	f.srv.cfg.OpenIDE = nil
	f.srv.cfg.OpenPR = nil
	f.srv.cfg.CheatSheet = "http://127.0.0.1:9000/cheat.html"
	f.srv.cfg.StripPrefix = "acme-"
	f.srv.cfg.EmbedOrigin = "http://127.0.0.1:9000"
	withCheatSheet := f.do(request(http.MethodGet, "/api/config", ""))

	assert.Equal(t, http.StatusOK, withIDE.Code)
	assert.JSONEq(t, `{"ide":true,"pr":true,"cheatSheet":false,"stripPrefix":"","embedOrigin":"","primaryActions":[],"secondaryActions":[]}`, withIDE.Body.String())
	assert.JSONEq(t, `{"ide":false,"pr":false,"cheatSheet":true,"stripPrefix":"acme-","embedOrigin":"http://127.0.0.1:9000","primaryActions":[],"secondaryActions":[]}`, withCheatSheet.Body.String())
}

func TestDiffOutsideARepository(t *testing.T) {
	f := newFixture()
	f.diffErr = gitdiff.ErrNoBase

	w := f.do(request(http.MethodGet, "/api/session/"+sessionID+"/diff", ""))

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, codeConflict, errorCode(t, w))
}

func TestRepos(t *testing.T) {
	f := newFixture()

	w := f.do(request(http.MethodGet, "/api/repos", ""))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `[{"name":"acme-contact","dir":"/work/acme-contact"}]`, w.Body.String())
}

func TestStream(t *testing.T) {
	f := newFixture()
	ts := httptest.NewServer(f.srv)
	defer ts.Close()
	r, err := http.NewRequest(http.MethodGet, ts.URL+"/api/stream", http.NoBody)
	require.NoError(t, err)
	r.Host = host
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: testToken})

	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test response body
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	select {
	case <-f.sessions.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not subscribe")
	}
	f.sessions.changes <- session.Change{Session: f.sessions.infos[sessionID], Live: true}

	reader := bufio.NewReader(resp.Body)
	event, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "event: change\n", event)
	data, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(data, "data: "))
	var change session.Change
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &change))
	assert.True(t, change.Live)
	assert.Equal(t, sessionID, change.Session.ID)
}
