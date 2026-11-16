package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

// gitTimeout covers a slow pre-commit hook or push; a hung ssh or credential prompt is cut off here.
const gitTimeout = 2 * time.Minute

type commitRequest struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

// gitResponse is what the command printed, and for Open PR the pull request's URL.
type gitResponse struct {
	Output string `json:"output"`
	URL    string `json:"url,omitempty"`
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if !decode(w, r, &req) {
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeValidation(w, "Validation error.", map[string]string{"message": "Write a commit message."})
		return
	}
	if len(req.Paths) == 0 {
		writeValidation(w, "Validation error.", map[string]string{"paths": "Nothing to commit."})
		return
	}
	dir, ok := s.gitDir(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	out, err := s.cfg.Commit(ctx, dir, message, req.Paths)
	s.gitResult(w, gitResponse{Output: out}, err)
}

func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.gitDir(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	out, err := s.cfg.Push(ctx, dir)
	s.gitResult(w, gitResponse{Output: out}, err)
}

func (s *Server) openPR(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OpenPR == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "gh is not installed, so the app cannot open pull requests.")
		return
	}
	dir, ok := s.gitDir(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	url, err := s.cfg.OpenPR(ctx, dir)
	s.gitResult(w, gitResponse{URL: url}, err)
}

// gitDir is the session's folder. A commit waits for Claude's turn to end, so it never takes a half-made edit;
// a push only sends what is already committed.
func (s *Server) gitDir(w http.ResponseWriter, r *http.Request, idle bool) (string, bool) {
	info, err := s.cfg.Sessions.Get(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return "", false
	}
	if info.Dir == "" {
		writeError(w, http.StatusConflict, codeConflict, unknownFolder)
		return "", false
	}
	if idle && info.State.Kind == state.KindWorking {
		writeError(w, http.StatusConflict, codeConflict, "Claude is working in this folder. Commit once the turn ends.")
		return "", false
	}
	return info.Dir, true
}

// gitResult reports a failed git or gh as a 409 with what it printed: a rejected hook, a rejected push or a
// missing login is something to read and fix, not a server fault.
func (s *Server) gitResult(w http.ResponseWriter, res gitResponse, err error) {
	if err != nil {
		writeError(w, http.StatusConflict, codeConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
