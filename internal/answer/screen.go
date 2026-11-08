package answer

import (
	"errors"
	"strconv"
	"strings"
)

// The screen text and patterns this file matches on are in wording.go.
var ErrNoMenuOnScreen = errors.New("no permission menu with a No entry on screen")

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
