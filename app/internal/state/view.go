package state

import "time"

// timeLayout is the house datetime format for API fields: UTC without an offset (api-guidelines.md).
const timeLayout = "2006-01-02 15:04:05"

// View is a session's state as the page receives it.
type View struct {
	Kind           Kind        `json:"kind"`
	NeedsYou       bool        `json:"needsYou"`
	CanAnswer      bool        `json:"canAnswer"`
	ChangedAt      string      `json:"changedAt"`
	Seq            int         `json:"seq"`
	PermissionMode string      `json:"permissionMode,omitempty"`
	Activity       string      `json:"activity,omitempty"`
	Questions      []Question  `json:"questions,omitempty"`
	Permission     *Permission `json:"permission,omitempty"`
	LastMessage    string      `json:"lastMessage,omitempty"`
	Failed         bool        `json:"failed,omitempty"`
	FromScreen     bool        `json:"fromScreen,omitempty"`
}

func (s Session) View() View {
	return View{
		Kind:           s.Kind,
		NeedsYou:       s.NeedsYou(),
		CanAnswer:      s.CanAnswer(),
		ChangedAt:      FormatTime(s.Since),
		Seq:            s.Seq,
		PermissionMode: s.PermissionMode,
		Activity:       s.Activity,
		Questions:      s.Questions,
		Permission:     s.Permission,
		LastMessage:    s.LastMessage,
		Failed:         s.Failed,
		FromScreen:     s.FromScreen,
	}
}

// CanAnswer reports whether the app may answer this state with keystrokes. A question with several parts or with
// multi-select has key sequences the spike did not cover, so the page sends the user to the terminal instead.
func (s Session) CanAnswer() bool {
	switch s.Kind {
	case KindTrust, KindPermission:
		return true
	case KindQuestion:
		return len(s.Questions) == 1 && !s.Questions[0].MultiSelect
	default:
		return false
	}
}

// FormatTime renders t in the API's datetime format; the zero time becomes "".
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}
