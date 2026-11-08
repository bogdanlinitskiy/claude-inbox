package answer

import "regexp"

// This file is everything the app knows about how Claude Code words and draws its screen. Nothing else in the
// package matches on screen text, so when a Claude Code release changes its output, this is the only file to
// re-read.
//
// None of it is guessed: each pattern is matched against a recorded screen in testdata/screens, and those
// fixtures are what fails first when the wording moves. Re-capture them, then fix what the tests point at.
//
// Where the screen is wrong, the cost is silent: Deny picks the wrong entry or refuses, the trust question goes
// unnoticed, and menus no hook reports stop being read. The app does not fall over, it just stops helping.

const (
	// menuFooter ends every menu Claude Code draws ("Esc to cancel · Tab to amend", "Enter to select · … · Esc to
	// cancel"). A menu needs it: a past prompt written as a numbered list shows as "❯ 1. …" in the scrollback too,
	// but with no footer under it.
	menuFooter = "Esc to cancel"

	// permissionQuestion opens a permission menu, in several wordings: "proceed?" (Bash), "make this edit to
	// <file>?" (Edit), "allow Claude to fetch this content?" (WebFetch).
	permissionQuestion = "Do you want to "

	// trustQuestion is the folder-trust screen, which Claude Code shows before any hook can fire.
	trustQuestion = "Is this a project you created or one you trust?"
)

var (
	// selectedEntry is the entry the cursor is on: "❯ 1. Yes". The prompt line of an idle session is "❯" followed
	// by a no-break space, so a digit typed there ("❯ 1") never matches: it has no dot.
	selectedEntry = regexp.MustCompile(`^[\s\x{00a0}]*❯[\s\x{00a0}]*(\d+)\.\s+(\S.*)$`)

	// menuEntry is any numbered entry, selected or not.
	menuEntry = regexp.MustCompile(`^[\s\x{00a0}]*(?:❯[\s\x{00a0}]*)?(\d+)\.\s+(\S.*)$`)

	// separatorLine is a rule or a blank line between entries, which never carries a label.
	separatorLine = regexp.MustCompile(`^[\s\x{00a0}─━╌┄═-]*$`)

	// noEntry is "   4. No" or "❯ 4. No", optionally followed by ", and tell Claude …" in other Claude Code
	// versions. Deny reads its number off the screen, because the number moves with the permission mode.
	noEntry = regexp.MustCompile(`^\s*(?:❯\s*)?(\d+)\.\s+No(?:\s*$|,)`)
)
