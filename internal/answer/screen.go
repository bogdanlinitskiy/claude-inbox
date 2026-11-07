package answer

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Permission menus ask "Do you want to …?" in several wordings: "proceed?" (Bash), "make this edit to <file>?"
	// (Edit), "allow Claude to fetch this content?" (WebFetch).
	permissionQuestion = "Do you want to "
	trustQuestion      = "Is this a project you created or one you trust?"
)

var (
	ErrNoMenuOnScreen = errors.New("no permission menu with a No entry on screen")

	// "   4. No" or "❯ 4. No", optionally followed by ", and tell Claude …" in other Claude Code versions.
	noEntry = regexp.MustCompile(`^\s*(?:❯\s*)?(\d+)\.\s+No(?:\s*$|,)`)
)

// NoOptionNumber reads the number of the "No" entry of the permission menu on screen. Only lines after the last
// "Do you want to …" question count, so older output above the menu cannot match.
func NoOptionNumber(screen string) (int, error) {
	lines := strings.Split(screen, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, permissionQuestion) {
			start = i
		}
	}
	if start < 0 {
		return 0, ErrNoMenuOnScreen
	}
	for _, line := range lines[start+1:] {
		if m := noEntry.FindStringSubmatch(line); m != nil {
			return strconv.Atoi(m[1])
		}
	}
	return 0, ErrNoMenuOnScreen
}

// IsTrustPrompt reports whether Claude Code is asking whether to trust the folder, which it does before any hook.
func IsTrustPrompt(screen string) bool {
	return strings.Contains(screen, trustQuestion)
}
