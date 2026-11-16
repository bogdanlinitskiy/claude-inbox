package gitdiff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const remote = "origin"

var (
	ErrDetached      = errors.New("HEAD is detached: check out a branch first")
	ErrDefaultBranch = errors.New("this is the default branch: push and open pull requests from a feature branch")
)

// Commit commits exactly paths, as they are in the working tree: changed, deleted or untracked. Other changes,
// staged or not, stay out of the commit. It answers what git printed.
func Commit(ctx context.Context, dir, message string, paths []string) (string, error) {
	if _, err := output(ctx, dir, "git", append([]string{"add", "--all", "--"}, paths...)...); err != nil {
		return "", err
	}
	return output(ctx, dir, "git", append([]string{"commit", "--message", message, "--"}, paths...)...)
}

// Push pushes the current branch to the branch of the same name on origin and tracks it. Never forced.
func Push(ctx context.Context, dir string) (string, error) {
	if _, err := featureBranch(ctx, dir); err != nil {
		return "", err
	}
	return output(ctx, dir, "git", "push", "--set-upstream", remote, "HEAD")
}

// OpenPR pushes the current branch, then answers the URL of its pull request: the open one, or a new draft whose
// title and body gh fills from the commits.
func OpenPR(ctx context.Context, dir string) (string, error) {
	branch, err := featureBranch(ctx, dir)
	if err != nil {
		return "", err
	}
	if _, err := Push(ctx, dir); err != nil {
		return "", err
	}
	// gh exits 1 when the branch has no pull request yet.
	if url, err := output(ctx, dir, "gh", "pr", "view", branch, "--json", "url", "--jq", ".url"); err == nil {
		return strings.TrimSpace(url), nil
	}
	// --head: gh neither pushes nor asks where to; the branch is pushed above.
	out, err := output(ctx, dir, "gh", "pr", "create", "--draft", "--fill", "--head", branch)
	if err != nil {
		return "", err
	}
	// gh prints the URL last, after any warnings.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1], nil
}

// featureBranch is the checked-out branch, refused when it is the default branch or HEAD is detached.
func featureBranch(ctx context.Context, dir string) (string, error) {
	branch, err := CurrentBranch(ctx, dir)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", ErrDetached
	}
	base, err := DefaultBase(ctx, dir)
	if err != nil {
		return "", err
	}
	if branch == strings.TrimPrefix(base, remote+"/") {
		return "", ErrDefaultBranch
	}
	return branch, nil
}

// output runs a command in dir and answers everything it printed; a failure carries that output, so a rejected
// hook or push reads as git wrote it. Paths are taken literally, never as patterns.
func output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1", "GH_PROMPT_DISABLED=1", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
