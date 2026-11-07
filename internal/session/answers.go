package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/answer"
	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

var (
	ErrStale         = errors.New("the session has moved on since this answer was chosen")
	ErrInvalidAnswer = errors.New("an answer needs exactly one of option, text, allow or trust")
	// ErrNothingOnScreen: the menu to answer is not on the screen any more, so no key is sent. Most often an
	// earlier answer already reached Claude and no hook has said so yet.
	ErrNothingOnScreen = errors.New("nothing to answer on the screen")
	// ErrAlreadySent: an answer to this state is on its way; a second one would land in Claude's prompt.
	ErrAlreadySent = errors.New("an answer to this is already on its way")
)

// AnswerRequest answers what the session waits on. Seq is the state the user saw; exactly one of the other fields
// is set.
type AnswerRequest struct {
	Seq    int     `json:"seq"`
	Option *int    `json:"option"`
	Text   *string `json:"text"`
	Allow  *bool   `json:"allow"`
	Trust  *bool   `json:"trust"`
}

func (a AnswerRequest) fieldsSet() int {
	n := 0
	for _, set := range []bool{a.Option != nil, a.Text != nil, a.Allow != nil, a.Trust != nil} {
		if set {
			n++
		}
	}
	return n
}

// Answer sends the user's answer to the question, permission or trust screen the session shows, if it still
// shows the state with a.Seq.
func (m *Manager) Answer(ctx context.Context, id string, a AnswerRequest) (Info, error) {
	if a.fieldsSet() != 1 {
		return Info{}, ErrInvalidAnswer
	}
	e, s, err := m.lookup(id)
	if err != nil {
		return Info{}, err
	}
	if a.Seq != s.Seq {
		return Info{}, ErrStale
	}
	p, err := e.tmuxOf()
	if err != nil {
		return Info{}, err
	}
	e.send.Lock()
	defer e.send.Unlock()
	if m.answerOnItsWay(e, a.Seq) {
		return Info{}, ErrAlreadySent
	}
	screen, err := p.tmux.Capture(ctx, p.target)
	if err != nil {
		return Info{}, err
	}
	wait, err := onScreen(s, screen, a.Trust != nil)
	if err != nil {
		return Info{}, err
	}
	wait.seq = s.Seq

	switch {
	case a.Option != nil:
		return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.Option(s, *a.Option) }, wait)
	case a.Text != nil && s.Kind == state.KindPermission:
		if err := m.deny(ctx, e, s, screen); err != nil {
			return Info{}, err
		}
		idle := state.Session{Kind: state.KindIdle}
		return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.Message(idle, *a.Text) }, pending{retryEnter: true})
	case a.Text != nil:
		wait.retryEnter = true
		return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.FreeText(s, *a.Text) }, wait)
	case a.Allow != nil && *a.Allow:
		return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.Allow(s) }, wait)
	case a.Allow != nil:
		if err := m.deny(ctx, e, s, screen); err != nil {
			return Info{}, err
		}
		return m.Get(id)
	case *a.Trust:
		return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.Trust(s, true) }, wait)
	default:
		steps, err := answer.Trust(s, false)
		if err != nil {
			return Info{}, err
		}
		if err := m.execute(ctx, p, steps); err != nil {
			return Info{}, err
		}
		return m.Get(id)
	}
}

// Message sends a prompt to an idle, working or finished session. A working session queues it.
func (m *Manager) Message(ctx context.Context, id, prompt string) (Info, error) {
	e, s, err := m.lookup(id)
	if err != nil {
		return Info{}, err
	}
	if _, err := e.tmuxOf(); err != nil {
		return Info{}, err
	}
	e.send.Lock()
	defer e.send.Unlock()
	return m.sendExpectingHook(ctx, e, func() ([]answer.Step, error) { return answer.Message(s, prompt) }, pending{retryEnter: true})
}

// Interrupt stops the running turn. Claude Code fires no hook for it, so the app records it.
func (m *Manager) Interrupt(ctx context.Context, id string) (Info, error) {
	e, s, err := m.lookup(id)
	if err != nil {
		return Info{}, err
	}
	p, err := e.tmuxOf()
	if err != nil {
		return Info{}, err
	}
	e.send.Lock()
	defer e.send.Unlock()
	steps, err := answer.Interrupt(s)
	if err != nil {
		return Info{}, err
	}
	if err := m.execute(ctx, p, steps); err != nil {
		return Info{}, err
	}
	if err := m.cfg.Store.AppendAppEvent(id, state.EventInterrupt, m.cfg.Now()); err != nil {
		return Info{}, err
	}
	return m.Get(id)
}

// onScreen checks that what the answer is for is still on the screen, and returns what to wait for once it is
// sent: that menu, or the trust screen, leaving the screen.
func onScreen(s state.Session, screen string, trust bool) (pending, error) {
	if trust {
		if !answer.IsTrustPrompt(screen) {
			return pending{}, ErrNothingOnScreen
		}
		return pending{trust: true}, nil
	}
	menu, ok := answer.ParseMenu(screen)
	if !ok {
		return pending{}, ErrNothingOnScreen
	}
	return pending{menu: &menu}, nil
}

// answerOnItsWay reports whether an answer to the state with seq was sent and is not settled yet.
func (m *Manager) answerOnItsWay(e *entry, seq int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return e.pending != nil && e.pending.isAnswer() && e.pending.seq == seq
}

// deny picks No on the permission menu. Claude Code fires no hook for it, so the app records it.
func (m *Manager) deny(ctx context.Context, e *entry, s state.Session, screen string) error {
	steps, err := answer.Deny(s, screen)
	if err != nil {
		return err
	}
	if err := m.execute(ctx, e.pane, steps); err != nil {
		return err
	}
	return m.cfg.Store.AppendAppEvent(e.meta.ID, state.EventDenied, m.cfg.Now())
}

// sendExpectingHook sends the steps and waits, in the background, for any hook of the session to prove Claude
// Code reacted (wait says what else proves it, see settle).
func (m *Manager) sendExpectingHook(ctx context.Context, e *entry, build func() ([]answer.Step, error), wait pending) (Info, error) {
	steps, err := build()
	if err != nil {
		return Info{}, err
	}
	wait.sentAt = m.cfg.Now()
	if err := m.execute(ctx, e.pane, steps); err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e.pending = &wait
	e.delivery = DeliveryPending
	m.publish(e, false, true)
	return e.info(m.cfg.TicketPattern), nil
}

// checkPending settles the sends that no hook has confirmed within the delivery timeout.
func (m *Manager) checkPending(ctx context.Context) {
	now := m.cfg.Now()
	type due struct {
		e *entry
		p *pending
	}
	var dues []due
	m.mu.Lock()
	for _, e := range m.entries {
		if p := e.pending; p != nil && now.Sub(p.sentAt) >= m.cfg.DeliveryTimeout {
			dues = append(dues, due{e: e, p: p})
		}
	}
	m.mu.Unlock()
	for _, d := range dues {
		m.settle(ctx, d.e, d.p, now)
	}
}

// settle decides what a send came to when no hook confirmed it in time. An answer landed if what it answered left
// the screen: Allow on a long command fires no hook until the command ends. Otherwise one more Enter where that is
// safe (the send ended with text and Enter; on a menu, Enter would pick the selected entry), then the send counts
// as not delivered.
func (m *Manager) settle(ctx context.Context, e *entry, p *pending, now time.Time) {
	landed := false
	if p.isAnswer() {
		screen, err := e.pane.tmux.Capture(ctx, e.pane.target)
		if err != nil {
			m.cfg.Logger.Error("capture screen to check an answer", "session", e.meta.ID, "err", err)
		} else {
			landed = !p.stillOnScreen(screen)
		}
	}

	m.mu.Lock()
	if e.pending != p {
		// A hook settled it meanwhile.
		m.mu.Unlock()
		return
	}
	retry := false
	switch {
	case landed:
		if err := m.appendAppEvent(e.meta.ID, state.EventAnswered, state.SeqPayload{Seq: p.seq}); err != nil {
			m.cfg.Logger.Error("record answer", "session", e.meta.ID, "err", err)
		}
		e.pending = nil
		e.delivery = DeliveryNone
	case p.retryEnter && !p.retried:
		p.retried = true
		p.sentAt = now
		retry = true
	default:
		e.pending = nil
		e.delivery = DeliveryFailed
	}
	if !retry {
		m.publish(e, false, true)
	}
	m.mu.Unlock()

	if retry {
		if err := e.pane.tmux.SendKeys(ctx, e.pane.target, keyEnter); err != nil {
			m.cfg.Logger.Error("retry Enter", "session", e.meta.ID, "err", err)
		}
	}
}

// appendAppEvent records an app event with a payload.
func (m *Manager) appendAppEvent(id, event string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return m.cfg.Store.Append(id, store.Record{TS: m.cfg.Now(), Source: store.SourceApp, Event: event, Payload: body})
}

// execute runs the steps in order, pausing between typed or pasted text and the key that follows it.
func (m *Manager) execute(ctx context.Context, p pane, steps []answer.Step) error {
	afterText := false
	for _, step := range steps {
		if step.Text != "" {
			if err := p.tmux.SendText(ctx, p.target, step.Text); err != nil {
				return err
			}
			afterText = true
			continue
		}
		if step.Paste != "" {
			if err := p.tmux.PasteText(ctx, p.target, step.Paste); err != nil {
				return err
			}
			afterText = true
			continue
		}
		if afterText {
			if err := sleep(ctx, m.cfg.KeyPause); err != nil {
				return err
			}
		}
		if err := p.tmux.SendKeys(ctx, p.target, step.Keys...); err != nil {
			return err
		}
		afterText = false
	}
	return nil
}
