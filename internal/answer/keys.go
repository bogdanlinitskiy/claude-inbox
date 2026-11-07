// Package answer turns what the user picks in the page into the keystrokes Claude Code expects
// (docs/architecture.md, "Answers to keys").
package answer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const (
	keyEnter      = "Enter"
	keyEscape     = "Escape"
	keyDown       = "Down"
	keyAllow      = "1"
	keyClearInput = "C-c"
)

var (
	ErrWrongState    = errors.New("the session is not waiting for this kind of answer")
	ErrNotAnswerable = errors.New("this question has to be answered in the terminal")
	ErrNoSuchOption  = errors.New("no such option")
	ErrEmptyText     = errors.New("the text is empty")
	ErrMultiline     = errors.New("the text has line breaks, which would submit it early; use the terminal")
)

// Step is one tmux call: key names for send-keys, text typed literally, or text pasted in one piece.
type Step struct {
	Keys  []string
	Text  string
	Paste string
}

func keys(names ...string) Step {
	return Step{Keys: names}
}

func text(t string) Step {
	return Step{Text: t}
}

func paste(t string) Step {
	return Step{Paste: t}
}

// Option picks option i (0-based) of the question on screen. The number key selects at once, without Enter.
func Option(s state.Session, i int) ([]Step, error) {
	q, err := singleQuestion(s)
	if err != nil {
		return nil, err
	}
	if i < 0 || i >= len(q.Options) {
		return nil, fmt.Errorf("%w: %d of %d", ErrNoSuchOption, i, len(q.Options))
	}
	return []Step{keys(strconv.Itoa(i + 1))}, nil
}

// FreeText answers the question on screen through its "Type something" entry, which comes right after the options.
// A menu read from the screen has no such entry.
func FreeText(s state.Session, answer string) ([]Step, error) {
	q, err := singleQuestion(s)
	if err != nil {
		return nil, err
	}
	if s.FromScreen {
		return nil, ErrNotAnswerable
	}
	if err := checkText(answer); err != nil {
		return nil, err
	}
	if isMultiline(answer) {
		return nil, ErrMultiline
	}
	return []Step{keys(strconv.Itoa(len(q.Options) + 1)), text(answer), keys(keyEnter)}, nil
}

func Allow(s state.Session) ([]Step, error) {
	if s.Kind != state.KindPermission {
		return nil, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	return []Step{keys(keyAllow)}, nil
}

// Deny picks the "No" entry of the permission menu on screen. Its number depends on the menu, so it is read from
// the screen. Deny fires no hook; the caller records state.EventDenied.
func Deny(s state.Session, screen string) ([]Step, error) {
	if s.Kind != state.KindPermission {
		return nil, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	n, err := NoOptionNumber(screen)
	if err != nil {
		return nil, err
	}
	return []Step{keys(strconv.Itoa(n))}, nil
}

// Message types a prompt and submits it. While Claude works, it queues the prompt. A prompt with line breaks is
// pasted, since typing a line break would submit the prompt at that point.
//
// An idle or finished session gets its input box cleared first, so text half-typed in the terminal does not end up
// in front of the prompt. A working session keeps it: there C-c would stop the turn.
func Message(s state.Session, prompt string) ([]Step, error) {
	var steps []Step
	switch s.Kind {
	case state.KindIdle, state.KindDone:
		steps = append(steps, keys(keyClearInput))
	case state.KindWorking:
	default:
		return nil, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	if err := checkText(prompt); err != nil {
		return nil, err
	}
	if isMultiline(prompt) {
		steps = append(steps, paste(prompt))
	} else {
		steps = append(steps, text(prompt))
	}
	return append(steps, keys(keyEnter)), nil
}

// Interrupt stops the running turn. It fires no hook; the caller records state.EventInterrupt.
func Interrupt(s state.Session) ([]Step, error) {
	if s.Kind != state.KindWorking {
		return nil, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	return []Step{keys(keyEscape)}, nil
}

// Trust answers the trust screen, where "No, exit" is selected first.
func Trust(s state.Session, trust bool) ([]Step, error) {
	if s.Kind != state.KindTrust {
		return nil, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	if trust {
		return []Step{keys(keyDown, keyEnter)}, nil
	}
	return []Step{keys(keyEnter)}, nil
}

func singleQuestion(s state.Session) (state.Question, error) {
	if s.Kind != state.KindQuestion {
		return state.Question{}, fmt.Errorf("%w: %s", ErrWrongState, s.Kind)
	}
	if !s.CanAnswer() {
		return state.Question{}, ErrNotAnswerable
	}
	return s.Questions[0], nil
}

func checkText(t string) error {
	if strings.TrimSpace(t) == "" {
		return ErrEmptyText
	}
	return nil
}

func isMultiline(t string) bool {
	return strings.ContainsAny(t, "\r\n")
}
