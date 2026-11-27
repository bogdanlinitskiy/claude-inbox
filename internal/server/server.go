// Package server is the HTTP side of the app: the page, the JSON API over the session manager, and the change
// stream. It listens on 127.0.0.1 only, and every request has to come from this user's page (auth.go).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/bogdanlinitskiy/claude-inbox/internal/action"
	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/transcript"
)

const (
	codeValidation   = "VALIDATION_CONSTRAINT"
	codeAccessDenied = "ACCESS_DENIED"
	codeForbidden    = "FORBIDDEN"
	codeNotFound     = "NOT_FOUND"
	codeConflict     = "CONFLICT"
	// codeNothingOnScreen: the answer found nothing to answer on the session's screen; the page locks its buttons.
	codeNothingOnScreen = "NOTHING_ON_SCREEN"
	codeServerError     = "SERVER_ERROR"

	maxBodyBytes = 64 << 10
)

// Sessions is what the server needs from session.Manager.
type Sessions interface {
	List() []session.Info
	Get(id string) (session.Info, error)
	Start(ctx context.Context, req session.StartRequest) (session.Info, error)
	StartGroup(ctx context.Context, req session.StartGroupRequest) ([]session.Info, error)
	Send(ctx context.Context, fromID, to, text string) (session.Info, error)
	GroupMessages(group string) ([]store.GroupMessage, error)
	Close(ctx context.Context, id string) error
	Answer(ctx context.Context, id string, a session.AnswerRequest) (session.Info, error)
	Message(ctx context.Context, id, prompt string) (session.Info, error)
	Interrupt(ctx context.Context, id string) (session.Info, error)
	Conversations() ([]session.Conversation, error)
	ResumeConversation(ctx context.Context, claudeSessionID string) (session.Info, error)
	Preview(claudeSessionID string) (transcript.Preview, error)
	Rename(id, title string) (session.Info, error)
	Pin(id string, pinned bool) (session.Info, error)
	Repos() ([]session.Repo, error)
	Subscribe() (<-chan session.Change, func())
	Attach(id string) ([]string, error)
}

type Config struct {
	Port     int
	Token    string
	Sessions Sessions
	Diff     func(ctx context.Context, dir string) (gitdiff.Diff, error)
	// OpenIDE opens a folder in the IDE; nil when no IDE is set, which hides the page's button.
	OpenIDE func(ctx context.Context, dir string) error
	// Commit, Push and OpenPR are the Diff tab's buttons; OpenPR is nil without gh, which hides its button.
	Commit func(ctx context.Context, dir, message string, paths []string) (string, error)
	Push   func(ctx context.Context, dir string) (string, error)
	OpenPR func(ctx context.Context, dir string) (string, error)
	// Web holds the page; index.html is served at /.
	Web fs.FS
	// StripPrefix is left out of repository names on the page.
	StripPrefix string
	// EmbedOrigin is the one other origin, e.g. http://127.0.0.1:7700, that may show the page in a frame and set
	// its theme. Empty: only the app itself.
	EmbedOrigin string
	// PrimaryActions show over the reply box, SecondaryActions in the Actions menu; an action may be in both.
	PrimaryActions   []action.Action
	SecondaryActions []action.Action
	Logger           *slog.Logger
}

type Server struct {
	cfg            Config
	allowedHosts   map[string]bool
	allowedOrigins map[string]bool
	frameAncestors string
	handler        http.Handler
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg, allowedHosts: hostsFor(cfg.Port), allowedOrigins: originsFor(cfg.Port), frameAncestors: frameAncestors(cfg.EmbedOrigin)}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/sessions", s.listSessions)
	api.HandleFunc("POST /api/sessions", s.startSession)
	api.HandleFunc("POST /api/groups", s.startGroup)
	api.HandleFunc("GET /api/group/{id}/messages", s.groupMessages)
	api.HandleFunc("GET /api/session/{id}", s.getSession)
	api.HandleFunc("PATCH /api/session/{id}", s.renameSession)
	api.HandleFunc("DELETE /api/session/{id}", s.closeSession)
	api.HandleFunc("PUT /api/session/{id}/pin", s.pin)
	api.HandleFunc("DELETE /api/session/{id}/pin", s.pin)
	api.HandleFunc("POST /api/session/{id}/answer", s.answer)
	api.HandleFunc("POST /api/session/{id}/message", s.message)
	api.HandleFunc("POST /api/session/{id}/send", s.send)
	api.HandleFunc("POST /api/session/{id}/interrupt", s.interrupt)
	api.HandleFunc("POST /api/session/{id}/action", s.runAction)
	api.HandleFunc("GET /api/session/{id}/diff", s.diff)
	api.HandleFunc("POST /api/session/{id}/commit", s.commit)
	api.HandleFunc("POST /api/session/{id}/push", s.push)
	api.HandleFunc("POST /api/session/{id}/pr", s.openPR)
	api.HandleFunc("POST /api/session/{id}/open-ide", s.openIDE)
	api.HandleFunc("GET /api/session/{id}/terminal", s.terminal)
	api.HandleFunc("GET /api/conversations", s.conversations)
	api.HandleFunc("POST /api/conversation/{id}/resume", s.resumeConversation)
	api.HandleFunc("GET /api/conversation/{id}/preview", s.preview)
	api.HandleFunc("GET /api/repos", s.repos)
	api.HandleFunc("GET /api/config", s.pageConfig)
	api.HandleFunc("GET /api/stream", s.stream)

	mux := http.NewServeMux()
	mux.Handle("/api/", s.requireToken(api))
	mux.HandleFunc("GET /{$}", s.page)
	// No method in this pattern: "GET /" would overlap "/api/" with neither more specific, which ServeMux refuses.
	mux.Handle("/", s.requireToken(http.FileServerFS(cfg.Web)))
	s.handler = s.guard(mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// page trades the token from the printed link for a cookie, then serves the page to whoever has the cookie.
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if token := r.URL.Query().Get(tokenQuery); token != "" {
		if !s.tokenMatches(token) {
			writeError(w, http.StatusUnauthorized, codeAccessDenied, "This link is from an earlier run. Open the link that claude-inbox serve printed.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: TokenCookie, Value: s.cfg.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.hasToken(r) {
		writeError(w, http.StatusUnauthorized, codeAccessDenied, "Open the link that claude-inbox serve printed.")
		return
	}
	http.ServeFileFS(w, r, s.cfg.Web, "index.html")
}

// pageConfig tells the page which optional buttons have something behind them, how to shorten repository
// names, and the configured actions.
type pageConfig struct {
	IDE              bool         `json:"ide"`
	PR               bool         `json:"pr"`
	StripPrefix      string       `json:"stripPrefix"`
	EmbedOrigin      string       `json:"embedOrigin"`
	PrimaryActions   []pageAction `json:"primaryActions"`
	SecondaryActions []pageAction `json:"secondaryActions"`
}

func (s *Server) pageConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, pageConfig{
		IDE: s.cfg.OpenIDE != nil, PR: s.cfg.OpenPR != nil, StripPrefix: s.cfg.StripPrefix, EmbedOrigin: s.cfg.EmbedOrigin,
		PrimaryActions: pageActions(s.cfg.PrimaryActions), SecondaryActions: pageActions(s.cfg.SecondaryActions),
	})
}

type errorBody struct {
	Message string            `json:"message"`
	Errors  map[string]string `json:"errors,omitempty"`
	Code    string            `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// The status line is already out, so a failed write (the client went away) cannot be reported to anyone.
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Message: message, Code: code})
}

func writeValidation(w http.ResponseWriter, message string, fieldErrors map[string]string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Message: message, Errors: fieldErrors, Code: codeValidation})
}

// decode reads a JSON body into dst, refusing unknown fields and oversized bodies.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeValidation(w, "The request body is not valid JSON for this request.", nil)
		return false
	}
	return true
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.cfg.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, codeServerError, "Unexpected error. The details are in the claude-inbox log.")
}

func isNotFound(err error) bool {
	return errors.Is(err, session.ErrNotFound)
}
