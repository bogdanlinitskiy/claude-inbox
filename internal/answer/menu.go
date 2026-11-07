package answer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const (
	minMenuOptions = 2
	// menuFooter ends every menu Claude Code draws ("Esc to cancel · Tab to amend", "Enter to select · … · Esc to
	// cancel"). A menu needs it: a past prompt written as a numbered list shows as "❯ 1. …" in the scrollback too,
	// but with no footer under it.
	menuFooter = "Esc to cancel"
)

var (
	// The entry the cursor is on: "❯ 1. Yes". The prompt line of an idle session is "❯" followed by a no-break
	// space, so a digit typed there ("❯ 1") never matches: it has no dot.
	selectedEntry = regexp.MustCompile(`^[\s\x{00a0}]*❯[\s\x{00a0}]*(\d+)\.\s+(\S.*)$`)
	menuEntry     = regexp.MustCompile(`^[\s\x{00a0}]*(?:❯[\s\x{00a0}]*)?(\d+)\.\s+(\S.*)$`)
	separatorLine = regexp.MustCompile(`^[\s\x{00a0}─━╌┄═-]*$`)
)

// Menu is a numbered menu on Claude Code's screen: the question above it and its entries, numbered from 1.
type Menu struct {
	Question string
	Options  []state.Option
}

// Same reports whether two reads of the screen show the same menu.
func (m Menu) Same(other Menu) bool {
	if m.Question != other.Question || len(m.Options) != len(other.Options) {
		return false
	}
	for i := range m.Options {
		if m.Options[i] != other.Options[i] {
			return false
		}
	}
	return true
}

// AsQuestion is the menu as the state of a session asks it.
func (m Menu) AsQuestion() state.Question {
	return state.Question{Question: m.Question, Options: m.Options}
}

// ParseMenu reads the numbered menu Claude Code shows at the bottom of its screen: a permission prompt, a
// question, or one of the prompts no hook reports. It takes the last entry under the cursor (❯), the entries
// numbered 1…n around it up to the footer, the line under an entry as its description, and the nearest line above
// entry 1 as the question. The trust prompt has no numbers and is read by IsTrustPrompt instead.
func ParseMenu(screen string) (Menu, bool) {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	selected := -1
	for i, line := range lines {
		if selectedEntry.MatchString(line) {
			selected = i
		}
	}
	if selected < 0 {
		return Menu{}, false
	}
	first := -1
	for i := selected; i >= 0; i-- {
		if n, ok := entryNumber(lines[i]); ok && n == 1 {
			first = i
			break
		}
	}
	if first < 0 {
		return Menu{}, false
	}

	var menu Menu
	last := first
	described, footer := true, false
	for i := first; i < len(lines); i++ {
		line := lines[i]
		if strings.Contains(line, menuFooter) {
			footer = true
			break
		}
		if n, ok := entryNumber(line); ok {
			if n != len(menu.Options)+1 {
				break
			}
			menu.Options = append(menu.Options, state.Option{Label: strings.TrimSpace(menuEntry.FindStringSubmatch(line)[2])})
			last, described = i, false
			continue
		}
		if separatorLine.MatchString(line) {
			described = true
			continue
		}
		if !described {
			menu.Options[len(menu.Options)-1].Description = strings.TrimSpace(line)
			described = true
		}
	}
	if !footer || len(menu.Options) < minMenuOptions || selected > last {
		return Menu{}, false
	}
	for i := first - 1; i >= 0; i-- {
		if !separatorLine.MatchString(lines[i]) {
			menu.Question = strings.TrimSpace(lines[i])
			break
		}
	}
	return menu, true
}

func entryNumber(line string) (int, bool) {
	m := menuEntry.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}
