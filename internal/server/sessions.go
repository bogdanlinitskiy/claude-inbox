package server

import (
	"errors"
	"net/http"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
)

// unknownFolder: the global hook got no usable folder for a discovered session.
const unknownFolder = "The folder of this session is not known."

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Sessions.List())
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.Get(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var req session.StartRequest
	if !decode(w, r, &req) {
		return
	}
	info, err := s.cfg.Sessions.Start(r.Context(), req)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// startGroup answers with the sessions it started; when one of them failed to start, the others keep running and
// the error says which.
func (s *Server) startGroup(w http.ResponseWriter, r *http.Request) {
	var req session.StartGroupRequest
	if !decode(w, r, &req) {
		return
	}
	infos, err := s.cfg.Sessions.StartGroup(r.Context(), req)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, infos)
}

type sendRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

// send is how claude-inbox send, run by a session of a group, messages another session of that group.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !decode(w, r, &req) {
		return
	}
	info, err := s.cfg.Sessions.Send(r.Context(), r.PathValue("id"), req.To, req.Text)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) groupMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.cfg.Sessions.GroupMessages(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

type renameRequest struct {
	Title string `json:"title"`
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if !decode(w, r, &req) {
		return
	}
	info, err := s.cfg.Sessions.Rename(r.PathValue("id"), req.Title)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// pin pins the session on PUT and unpins it on DELETE.
func (s *Server) pin(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.Pin(r.PathValue("id"), r.Method == http.MethodPut)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Sessions.Close(r.Context(), r.PathValue("id")); err != nil {
		s.sessionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var req session.AnswerRequest
	if !decode(w, r, &req) {
		return
	}
	info, err := s.cfg.Sessions.Answer(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type messageRequest struct {
	Text string `json:"text"`
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var req messageRequest
	if !decode(w, r, &req) {
		return
	}
	info, err := s.cfg.Sessions.Message(r.Context(), r.PathValue("id"), req.Text)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.Interrupt(r.Context(), r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Sessions.Conversations()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) resumeConversation(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.ResumeConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	p, err := s.cfg.Sessions.Preview(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.Get(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	if info.Dir == "" {
		writeError(w, http.StatusConflict, codeConflict, unknownFolder)
		return
	}
	d, err := s.cfg.Diff(r.Context(), info.Dir)
	if errors.Is(err, gitdiff.ErrNoBase) {
		writeError(w, http.StatusConflict, codeConflict, "No diff: this folder is not a git repository with a master or main branch.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) openIDE(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OpenIDE == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "No IDE is set; start serve with --ide <command>.")
		return
	}
	info, err := s.cfg.Sessions.Get(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	if info.Dir == "" {
		writeError(w, http.StatusConflict, codeConflict, unknownFolder)
		return
	}
	if err := s.cfg.OpenIDE(r.Context(), info.Dir); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.cfg.Sessions.Repos()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// sessionError maps the manager's and the answer package's errors to responses the page can act on.
func (s *Server) sessionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case isNotFound(err):
		writeError(w, http.StatusNotFound, codeNotFound, "This session no longer exists.")
	case errors.Is(err, session.ErrNothingOnScreen):
		writeError(w, http.StatusConflict, codeNothingOnScreen, "Nothing to answer on the screen: the answer most likely reached Claude already. No key was sent.")
	case errors.Is(err, session.ErrAlreadySent):
		writeError(w, http.StatusConflict, codeConflict, "An answer is already on its way. Wait for Claude to react.")
	case errors.Is(err, session.ErrWorktree):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, session.ErrNoBranch), errors.Is(err, gitdiff.ErrBadBranch):
		writeValidation(w, "Validation error.", map[string]string{"branch": "Enter a valid branch name for the worktree."})
	case errors.Is(err, session.ErrNoWorktree):
		writeValidation(w, "Validation error.", map[string]string{"dir": "A workspace has no worktrees. Pick a repository."})
	case errors.Is(err, session.ErrStale):
		writeError(w, http.StatusConflict, codeConflict, "The session moved on before the answer arrived. Check what it asks now.")
	case errors.Is(err, answer.ErrWrongState), errors.Is(err, answer.ErrNoMenuOnScreen):
		writeError(w, http.StatusConflict, codeConflict, "The session is not waiting for this answer any more.")
	case errors.Is(err, answer.ErrNotAnswerable):
		writeError(w, http.StatusConflict, codeConflict, "Answer this question in the terminal.")
	case errors.Is(err, session.ErrNotInTmux):
		writeError(w, http.StatusConflict, codeConflict, "This session does not run in tmux. Answer it where it runs.")
	case errors.Is(err, session.ErrStillRunning):
		writeError(w, http.StatusConflict, codeConflict, "This conversation already runs in another session.")
	case errors.Is(err, session.ErrNoFolder):
		writeError(w, http.StatusConflict, codeConflict, "The folder of this conversation no longer exists.")
	case errors.Is(err, session.ErrNoRepos):
		writeValidation(w, "Validation error.", map[string]string{"dirs": "Pick at least one repository."})
	case errors.Is(err, session.ErrNoGroup):
		writeError(w, http.StatusConflict, codeConflict, "This session was not started in a group.")
	case errors.Is(err, session.ErrNotInGroup):
		writeValidation(w, "Validation error.", map[string]string{"to": "No session of that name in this group."})
	case errors.Is(err, session.ErrInvalidRepo):
		writeValidation(w, "Validation error.", map[string]string{"dir": "Pick one of the listed repositories."})
	case errors.Is(err, answer.ErrEmptyText):
		writeValidation(w, "Validation error.", map[string]string{"text": "Type something first."})
	case errors.Is(err, answer.ErrMultiline):
		writeValidation(w, "Validation error.", map[string]string{"text": "Line breaks would send it early. Use one line, or the terminal."})
	case errors.Is(err, answer.ErrNoSuchOption):
		writeValidation(w, "Validation error.", map[string]string{"option": "That option does not exist."})
	case errors.Is(err, session.ErrInvalidAnswer):
		writeValidation(w, "Send exactly one of option, text, allow or trust.", nil)
	default:
		s.serverError(w, r, err)
	}
}
