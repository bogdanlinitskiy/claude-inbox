package server

import (
	"net/http"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/action"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const (
	listPrimary   = "primary"
	listSecondary = "secondary"
	kindSend      = "send"
	kindRun       = "run"
)

// pageAction is a configured action as the page shows it. Text is the prompt or the command, shown and searched;
// the page asks for an action by its list and index, never by its text.
type pageAction struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Input   string `json:"input,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

func pageActions(list []action.Action) []pageAction {
	out := make([]pageAction, 0, len(list))
	for _, a := range list {
		p := pageAction{Label: a.Label, Kind: kindSend, Text: a.Send, Input: a.Input, Confirm: a.Confirm}
		if a.Run != "" {
			p.Kind, p.Text = kindRun, a.Run
		}
		out = append(out, p)
	}
	return out
}

type actionRequest struct {
	List  string `json:"list"`
	Index int    `json:"index"`
	Input string `json:"input"`
}

// actionResponse: a send action answers with the session, a run action with how its command ended.
type actionResponse struct {
	Session *session.Info  `json:"session,omitempty"`
	Run     *action.Result `json:"run,omitempty"`
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if !decode(w, r, &req) {
		return
	}
	a, ok := s.lookupAction(req.List, req.Index)
	if !ok {
		writeValidation(w, "Validation error.", map[string]string{"index": "No such action. Reload the page: the config may have changed."})
		return
	}
	if a.Input != "" && strings.TrimSpace(req.Input) == "" {
		writeValidation(w, "Validation error.", map[string]string{"input": "Enter " + a.Input + " first."})
		return
	}
	id := r.PathValue("id")
	if a.Send != "" {
		info, err := s.cfg.Sessions.Message(r.Context(), id, a.Text(req.Input))
		if err != nil {
			s.sessionError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, actionResponse{Session: &info})
		return
	}
	info, err := s.cfg.Sessions.Get(id)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	if info.Dir == "" {
		writeError(w, http.StatusConflict, codeConflict, unknownFolder)
		return
	}
	// A command in the folder Claude is editing could race its edits (git pull mid-change).
	if info.State.Kind == state.KindWorking {
		writeError(w, http.StatusConflict, codeConflict, "Claude is working in this folder. Run it once the turn ends.")
		return
	}
	res, err := action.Run(r.Context(), info.Dir, a.Run, req.Input)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{Run: &res})
}

func (s *Server) lookupAction(list string, index int) (action.Action, bool) {
	var actions []action.Action
	switch list {
	case listPrimary:
		actions = s.cfg.PrimaryActions
	case listSecondary:
		actions = s.cfg.SecondaryActions
	}
	if index < 0 || index >= len(actions) {
		return action.Action{}, false
	}
	return actions[index], true
}
