package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

const (
	// noticeSender names the app in the messages it sends a coordinator, as a session's name does in theirs.
	noticeSender = "claude-inbox"
	groupPrefix  = "[from "
	// maxQuote bounds what a notice quotes of a prompt or a reply; the coordinator can ask for the rest.
	maxQuote = 600
)

// turn is what started a worker's current turn.
type turn struct {
	start  time.Time
	prompt string
}

// notice is a message from the app to the coordinator of group, about one of its workers. Workers report through
// claude-inbox send; the app reports what they cannot, or did not: a turn they did not report, a failure, their
// end, a long silence (docs/architecture.md, "Session groups").
type notice struct {
	group string
	text  string
}

// watchWorker turns a live event of a worker into a notice for its coordinator when the event ends something
// the coordinator should know about. The caller holds m.mu.
func (m *Manager) watchWorker(e *entry, rec store.Record, before state.Session) {
	after := e.state
	name := e.meta.TmuxName
	switch {
	case after.Kind == state.KindEnded && before.Kind != state.KindEnded:
		m.notify(e, fmt.Sprintf("%s has ended: its session is gone, so it will not report again.", name))
	case after.Stalled && !before.Stalled:
		m.notify(e, fmt.Sprintf("%s has been working with no sign of activity for %s; it may have lost its connection. The user was alerted.",
			name, m.cfg.StallAfter))
	case after.Kind == state.KindDone && before.Kind != state.KindDone:
		m.turnEnded(e, after)
	default:
		if prompt, ok := promptOf(rec); ok {
			e.turn = &turn{start: rec.TS, prompt: prompt}
		}
	}
}

// turnEnded makes a notice for a failed turn, and for one the worker did not report with claude-inbox send.
func (m *Manager) turnEnded(e *entry, s state.Session) {
	t := e.turn
	e.turn = nil
	name := e.meta.TmuxName
	switch {
	case s.Failed:
		m.notify(e, fmt.Sprintf("%s stopped on an error. Its last message: %s", name, quote(s.LastMessage)))
	case t == nil || !e.lastSentAt.Before(t.start):
	case strings.HasPrefix(t.prompt, groupPrefix):
		m.notify(e, fmt.Sprintf("%s finished the turn a group message started without replying. Its last message: %s", name, quote(s.LastMessage)))
	default:
		m.notify(e, fmt.Sprintf("%s finished a turn the user started directly, without telling you. The user asked: %s. Its last message: %s",
			name, quote(t.prompt), quote(s.LastMessage)))
	}
}

// notify queues text for e's coordinator. The caller holds m.mu.
func (m *Manager) notify(e *entry, text string) {
	m.notices = append(m.notices, notice{group: e.meta.Group, text: groupPrefix + noticeSender + "] " + text})
}

// promptOf returns the prompt of a session's own UserPromptSubmit.
func promptOf(rec store.Record) (string, bool) {
	if rec.Source != store.SourceHook {
		return "", false
	}
	var p struct {
		HookEventName string `json:"hook_event_name"`
		AgentID       string `json:"agent_id"`
		Prompt        string `json:"prompt"`
	}
	if json.Unmarshal(rec.Payload, &p) != nil || p.HookEventName != "UserPromptSubmit" || p.AgentID != "" {
		return "", false
	}
	return p.Prompt, true
}

// quote puts text on one line, cut to maxQuote characters, in quotes.
func quote(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > maxQuote {
		text = string(r[:maxQuote]) + "…"
	}
	return "«" + text + "»"
}

// checkStalls records a stall for each working session of a group that has fired nothing for StallAfter. A lost
// connection fires no hook, so silence is the only sign of it.
func (m *Manager) checkStalls() {
	if m.cfg.StallAfter <= 0 {
		return
	}
	now := m.cfg.Now()
	type stall struct {
		id  string
		seq int
	}
	var due []stall
	m.mu.Lock()
	for id, e := range m.entries {
		if e.meta.Group != "" && e.state.Kind == state.KindWorking && !e.state.Stalled && now.Sub(e.lastEventAt) >= m.cfg.StallAfter {
			due = append(due, stall{id: id, seq: e.state.Seq})
		}
	}
	m.mu.Unlock()
	for _, s := range due {
		if err := m.appendAppEvent(s.id, state.EventStalled, state.SeqPayload{Seq: s.seq}); err != nil {
			m.cfg.Logger.Error("record a stall", "session", s.id, "err", err)
		}
	}
}

// deliverNotices sends each coordinator its waiting notices as one message. A coordinator that has a send on its
// way, or waits on the user, gets them on a later poll: a second message right after the first would start with
// Ctrl+C on a prompt that is still empty, which Claude Code reads as the first half of exit. Notices for a group
// whose coordinator is gone are dropped.
func (m *Manager) deliverNotices(ctx context.Context) {
	m.mu.Lock()
	byGroup := map[string][]string{}
	var order []string
	for _, n := range m.notices {
		if _, ok := byGroup[n.group]; !ok {
			order = append(order, n.group)
		}
		byGroup[n.group] = append(byGroup[n.group], n.text)
	}
	m.notices = nil
	type delivery struct {
		group string
		e     *entry
	}
	var ready []delivery
	for _, group := range order {
		c := m.coordinatorOf(group)
		switch {
		case c == nil:
		case c.pending != nil || c.state.NeedsYou():
			for _, text := range byGroup[group] {
				m.notices = append(m.notices, notice{group: group, text: text})
			}
		default:
			ready = append(ready, delivery{group: group, e: c})
		}
	}
	m.mu.Unlock()

	for _, d := range ready {
		text := strings.Join(byGroup[d.group], "\n")
		_, err := m.Message(ctx, d.e.meta.ID, text)
		if errors.Is(err, answer.ErrWrongState) {
			m.mu.Lock()
			m.notices = append(m.notices, notice{group: d.group, text: text})
			m.mu.Unlock()
			continue
		}
		if err != nil {
			m.cfg.Logger.Error("tell the coordinator", "group", d.group, "err", err)
			continue
		}
		msg := store.GroupMessage{SentAt: m.cfg.Now(), From: noticeSender, To: d.e.meta.TmuxName, Text: text}
		if err := m.cfg.Store.AppendGroupMessage(d.group, msg); err != nil {
			m.cfg.Logger.Error("log a group message", "group", d.group, "err", err)
		}
	}
}

// coordinatorOf returns the running coordinator of group, or nil. The caller holds m.mu.
func (m *Manager) coordinatorOf(group string) *entry {
	for _, e := range m.entries {
		if e.meta.Group == group && e.meta.Role == RoleCoordinator && !e.ended() {
			return e
		}
	}
	return nil
}
