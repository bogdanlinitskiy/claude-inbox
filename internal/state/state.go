// Package state turns a session's event records into its current state. Apply is pure: replaying the same
// records always gives the same state, which is how the app recovers after a restart.
package state

import (
	"encoding/json"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

type Kind string

const (
	KindTrust      Kind = "trust"
	KindIdle       Kind = "idle"
	KindWorking    Kind = "working"
	KindQuestion   Kind = "question"
	KindPermission Kind = "permission"
	KindDone       Kind = "done"
	KindEnded      Kind = "ended"
	KindUnknown    Kind = "unknown"
)

// App events, recorded by the app for things no hook reports.
const (
	EventTrustPrompt = "trust_prompt"
	EventInterrupt   = "interrupt"
	EventDenied      = "denied"
	EventPaneGone    = "pane_gone"
	EventClosed      = "closed"
	// EventProcessGone: the claude process of a discovered session no longer runs.
	EventProcessGone = "process_gone"
	// EventScreenPrompt: a menu no hook reported is on the screen; the payload is that menu as a Question.
	EventScreenPrompt = "screen_prompt"
	// EventScreenPromptGone: that menu left the screen without an answer from the app; the payload is SeqPayload.
	EventScreenPromptGone = "screen_prompt_gone"
	// EventAnswered: an answer from the app took the menu off the screen before any hook said so, as Allow does for
	// a long command; the payload is SeqPayload.
	EventAnswered = "answered"
)

// SeqPayload names the state an app event is about. The event changes nothing once the session has moved on, so
// it cannot touch what the session asks next.
type SeqPayload struct {
	Seq int `json:"seq"`
}

const (
	hookSessionStart       = "SessionStart"
	hookSessionEnd         = "SessionEnd"
	hookUserPromptSubmit   = "UserPromptSubmit"
	hookPreToolUse         = "PreToolUse"
	hookPostToolUse        = "PostToolUse"
	hookPostToolUseFailure = "PostToolUseFailure"
	hookPermissionRequest  = "PermissionRequest"
	hookPermissionDenied   = "PermissionDenied"
	hookStop               = "Stop"
	hookStopFailure        = "StopFailure"

	endReasonClear  = "clear"
	endReasonResume = "resume"

	toolAskUserQuestion = "AskUserQuestion"
	toolBash            = "Bash"

	maxDetailLength = 300
)

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

// Permission describes what a session asks to be allowed to do. Detail is the Bash command, or the most telling
// field of another tool's input (a file path, a URL), or that input as JSON.
type Permission struct {
	Tool        string `json:"tool"`
	Detail      string `json:"detail"`
	Description string `json:"description"`
}

type Session struct {
	Kind            Kind
	Since           time.Time
	Seq             int
	ClaudeSessionID string
	PermissionMode  string
	Activity        string
	Questions       []Question
	Permission      *Permission
	LastMessage     string
	Failed          bool
	// FromScreen: the question was read from the screen; no hook reported it.
	FromScreen bool
	// beforeScreen is the state to go back to once the question read from the screen is gone.
	beforeScreen *Session
}

// New is the state of a session the app has just started, before Claude Code reports anything.
func New(startedAt time.Time) Session {
	return Session{Kind: KindIdle, Since: startedAt}
}

// NeedsYou reports whether the session is waiting for an answer from the user.
func (s Session) NeedsYou() bool {
	return s.Kind == KindTrust || s.Kind == KindQuestion || s.Kind == KindPermission
}

type payload struct {
	HookEventName        string          `json:"hook_event_name"`
	SessionID            string          `json:"session_id"`
	AgentID              string          `json:"agent_id"`
	PermissionMode       string          `json:"permission_mode"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	Reason               string          `json:"reason"`
}

// IsMainHook reports whether rec is a hook event of the session itself, not of an internal agent. Any such event
// after a send proves Claude Code reacted to it.
func IsMainHook(rec store.Record) bool {
	if rec.Source != store.SourceHook || len(rec.Payload) == 0 {
		return false
	}
	var p payload
	return json.Unmarshal(rec.Payload, &p) == nil && p.HookEventName != "" && p.AgentID == ""
}

// Apply returns the state after rec. Seq goes up by one whenever anything visible changes, so an answer can be
// checked against the state the user saw.
func Apply(s Session, rec store.Record) Session {
	next := s
	switch rec.Source {
	case store.SourceApp:
		next = applyAppEvent(s, rec)
	case store.SourceHook:
		next = applyHook(s, rec)
	}
	if next.Kind != s.Kind {
		next.Since = rec.TS
	}
	if !sameVisibleState(s, next) {
		next.Seq = s.Seq + 1
	}
	return next
}

func applyAppEvent(s Session, rec store.Record) Session {
	switch rec.Event {
	case EventTrustPrompt:
		return become(s, KindTrust)
	case EventInterrupt, EventDenied:
		if s.Kind == KindEnded {
			return s
		}
		return become(s, KindIdle)
	case EventPaneGone, EventClosed, EventProcessGone:
		return become(s, KindEnded)
	case EventScreenPrompt:
		return screenPrompt(s, rec)
	case EventScreenPromptGone:
		if !forSeq(s, rec) {
			return s
		}
		return leaveScreenPrompt(s)
	case EventAnswered:
		if !forSeq(s, rec) {
			return s
		}
		return answered(s)
	default:
		return s
	}
}

func forSeq(s Session, rec store.Record) bool {
	var p SeqPayload
	return json.Unmarshal(rec.Payload, &p) == nil && p.Seq == s.Seq
}

// screenPrompt shows a menu read from the screen as a question, unless a hook already reported what the session
// waits on.
func screenPrompt(s Session, rec store.Record) Session {
	var q Question
	if s.Kind == KindEnded || (s.NeedsYou() && !s.FromScreen) || json.Unmarshal(rec.Payload, &q) != nil || len(q.Options) == 0 {
		return s
	}
	before := s
	if s.FromScreen {
		before = *s.beforeScreen
	}
	next := become(s, KindQuestion)
	next.Questions = []Question{q}
	next.FromScreen = true
	next.beforeScreen = &before
	return next
}

func leaveScreenPrompt(s Session) Session {
	if !s.FromScreen {
		return s
	}
	back := *s.beforeScreen
	back.Seq = s.Seq
	back.PermissionMode = s.PermissionMode
	return back
}

// answered is where an answer from the app leads when no hook reports it: Claude works on after a question or a
// permission, and starts after the trust screen.
func answered(s Session) Session {
	switch {
	case s.FromScreen:
		return leaveScreenPrompt(s)
	case s.Kind == KindQuestion, s.Kind == KindPermission:
		return become(s, KindWorking)
	case s.Kind == KindTrust:
		return become(s, KindIdle)
	default:
		return s
	}
}

func applyHook(s Session, rec store.Record) Session {
	var p payload
	if len(rec.Payload) == 0 || json.Unmarshal(rec.Payload, &p) != nil {
		return s
	}
	// Internal agents and subagents fire hooks in the same session; they never change what the session waits on.
	if p.AgentID != "" {
		return s
	}
	if p.HookEventName == hookSessionStart {
		next := become(s, KindIdle)
		next.ClaudeSessionID = p.SessionID
		next.PermissionMode = p.PermissionMode
		return next
	}
	if s.Kind == KindEnded {
		return s
	}
	if p.PermissionMode != "" {
		s.PermissionMode = p.PermissionMode
	}

	switch p.HookEventName {
	case hookUserPromptSubmit, hookPostToolUse, hookPostToolUseFailure, hookPermissionDenied:
		return become(s, KindWorking)
	case hookPreToolUse:
		next := become(s, KindWorking)
		next.Activity = activityOf(p)
		return next
	case hookPermissionRequest:
		return permissionRequest(s, p)
	case hookStop:
		next := become(s, KindDone)
		next.LastMessage = p.LastAssistantMessage
		return next
	case hookStopFailure:
		next := become(s, KindDone)
		next.LastMessage = p.LastAssistantMessage
		next.Failed = true
		return next
	case hookSessionEnd:
		// /clear and /resume end one conversation and start the next in the same process, tens of ms later; ending
		// the session on the first half would archive it between two polls while it keeps running.
		if p.Reason == endReasonClear || p.Reason == endReasonResume {
			return s
		}
		return become(s, KindEnded)
	default:
		return s
	}
}

func permissionRequest(s Session, p payload) Session {
	if p.ToolName == toolAskUserQuestion {
		var input struct {
			Questions []Question `json:"questions"`
		}
		if json.Unmarshal(p.ToolInput, &input) != nil || len(input.Questions) == 0 {
			return become(s, KindUnknown)
		}
		next := become(s, KindQuestion)
		next.Questions = input.Questions
		return next
	}
	var input map[string]any
	if json.Unmarshal(p.ToolInput, &input) != nil {
		return become(s, KindUnknown)
	}
	next := become(s, KindPermission)
	description, _ := input["description"].(string)
	next.Permission = &Permission{Tool: p.ToolName, Detail: detailOf(p.ToolName, input, p.ToolInput), Description: description}
	return next
}

// become switches to kind and drops everything that belonged to the previous kind. Since is set by Apply.
func become(s Session, kind Kind) Session {
	return Session{
		Kind:            kind,
		Since:           s.Since,
		Seq:             s.Seq,
		ClaudeSessionID: s.ClaudeSessionID,
		PermissionMode:  s.PermissionMode,
	}
}

func activityOf(p payload) string {
	var input map[string]any
	if json.Unmarshal(p.ToolInput, &input) != nil {
		return p.ToolName
	}
	if description, ok := input["description"].(string); ok && description != "" {
		return p.ToolName + ": " + description
	}
	if p.ToolName == toolBash {
		if command, ok := input["command"].(string); ok {
			return p.ToolName + ": " + truncate(command)
		}
	}
	return p.ToolName
}

func detailOf(tool string, input map[string]any, raw json.RawMessage) string {
	keys := []string{"file_path", "url", "path", "pattern"}
	if tool == toolBash {
		keys = []string{"command"}
	}
	for _, key := range keys {
		if value, ok := input[key].(string); ok && value != "" {
			return truncate(value)
		}
	}
	return truncate(string(raw))
}

func truncate(text string) string {
	runes := []rune(text)
	if len(runes) <= maxDetailLength {
		return text
	}
	return string(runes[:maxDetailLength]) + "…"
}

func sameVisibleState(a, b Session) bool {
	if a.Kind != b.Kind || a.Activity != b.Activity || a.LastMessage != b.LastMessage || a.Failed != b.Failed ||
		a.PermissionMode != b.PermissionMode || a.ClaudeSessionID != b.ClaudeSessionID || a.FromScreen != b.FromScreen {
		return false
	}
	if (a.Permission == nil) != (b.Permission == nil) || (a.Permission != nil && *a.Permission != *b.Permission) {
		return false
	}
	return sameQuestions(a.Questions, b.Questions)
}

func sameQuestions(a, b []Question) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Question != b[i].Question || a[i].Header != b[i].Header || a[i].MultiSelect != b[i].MultiSelect ||
			len(a[i].Options) != len(b[i].Options) {
			return false
		}
		for j := range a[i].Options {
			if a[i].Options[j] != b[i].Options[j] {
				return false
			}
		}
	}
	return true
}
