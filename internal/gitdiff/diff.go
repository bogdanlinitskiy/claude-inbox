// Package gitdiff builds what the Diff tab shows: everything a session changed in its repository since the branch
// left the default branch, committed or not, including files it created but never added. It also runs the tab's
// Commit, Push and Open PR (publish.go).
package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxPatchBytes     = 1 << 20
	maxUntrackedFiles = 500
	maxCountedBytes   = 4 << 20
	binarySniffBytes  = 8 << 10
)

// Default-branch candidates when origin/HEAD is not recorded, in order of preference.
var baseCandidates = []string{"origin/master", "master", "origin/main", "main"}

var (
	ErrNoBase    = errors.New("no default branch found (origin/HEAD, master or main)")
	ErrBadBranch = errors.New("not a valid branch name")
)

type File struct {
	Path      string `json:"path"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary,omitempty"`
	Untracked bool   `json:"untracked,omitempty"`
	// Uncounted marks an untracked file whose lines are not counted: too large, or not a regular file (a symlink).
	Uncounted bool `json:"uncounted,omitempty"`
}

type Diff struct {
	Base      string `json:"base"`
	Files     []File `json:"files"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
	// Uncommitted lists the paths a commit would take: changed, deleted or untracked, against HEAD.
	Uncommitted []string `json:"uncommitted"`
}

// Compute diffs the working tree of dir against its merge base with the default branch.
func Compute(ctx context.Context, dir string) (Diff, error) {
	base, err := DefaultBase(ctx, dir)
	if err != nil {
		return Diff{}, err
	}
	mergeBase, err := git(ctx, dir, "merge-base", "HEAD", base)
	if err != nil {
		return Diff{}, err
	}
	mergeBase = strings.TrimSpace(mergeBase)

	numstat, err := git(ctx, dir, "diff", "--no-renames", "--no-ext-diff", "--numstat", "-z", mergeBase)
	if err != nil {
		return Diff{}, err
	}
	files, err := parseNumstat(numstat)
	if err != nil {
		return Diff{}, err
	}
	patch, err := git(ctx, dir, "diff", "--no-renames", "--no-ext-diff", "--no-color", mergeBase)
	if err != nil {
		return Diff{}, err
	}
	untracked, untrackedCut, err := untrackedFiles(ctx, dir)
	if err != nil {
		return Diff{}, err
	}
	uncommitted, err := uncommittedPaths(ctx, dir)
	if err != nil {
		return Diff{}, err
	}

	// Never nil, so an unchanged repository encodes as "files": [] rather than null.
	all := make([]File, 0, len(files)+len(untracked))
	d := Diff{Base: base, Files: append(append(all, files...), untracked...), Patch: patch, Truncated: untrackedCut, Uncommitted: uncommitted}
	if len(d.Patch) > maxPatchBytes {
		d.Patch = d.Patch[:maxPatchBytes]
		d.Truncated = true
	}
	return d, nil
}

// DefaultBase is origin/HEAD when the clone recorded it, otherwise the first of origin/master, master,
// origin/main and main that exists.
func DefaultBase(ctx context.Context, dir string) (string, error) {
	if head, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(head), nil
	}
	for _, candidate := range baseCandidates {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", ErrNoBase
}

// AddWorktree checks branch out into a new worktree of the repository at repoDir, at dir: the branch as it is when
// it exists, otherwise a new branch from the default branch.
func AddWorktree(ctx context.Context, repoDir, dir, branch string) error {
	// A name starting with "-" would be read as an option.
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%w: %q", ErrBadBranch, branch)
	}
	if _, err := git(ctx, repoDir, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("%w: %q", ErrBadBranch, branch)
	}
	if _, err := git(ctx, repoDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = git(ctx, repoDir, "worktree", "add", dir, branch)
		return err
	}
	base, err := DefaultBase(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = git(ctx, repoDir, "worktree", "add", "-b", branch, dir, base)
	return err
}

// CurrentBranch is the checked-out branch of dir, or "" on a detached HEAD.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// parseNumstat reads `git diff --numstat -z` without renames: "added<TAB>deleted<TAB>path<NUL>", with "-" counts
// for binary files.
func parseNumstat(out string) ([]File, error) {
	var files []File
	for entry := range strings.SplitSeq(out, "\x00") {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected numstat entry %q", entry)
		}
		f := File{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
			files = append(files, f)
			continue
		}
		var err error
		if f.Added, err = strconv.Atoi(parts[0]); err != nil {
			return nil, fmt.Errorf("numstat added count %q: %w", parts[0], err)
		}
		if f.Deleted, err = strconv.Atoi(parts[1]); err != nil {
			return nil, fmt.Errorf("numstat deleted count %q: %w", parts[1], err)
		}
		files = append(files, f)
	}
	return files, nil
}

// uncommittedPaths reads `git status --porcelain -z` without renames: "XY path<NUL>" per changed path. Never nil,
// so a clean tree encodes as [].
func uncommittedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	const status = len("XY ")
	paths := []string{}
	for entry := range strings.SplitSeq(out, "\x00") {
		if len(entry) > status {
			paths = append(paths, entry[status:])
		}
	}
	return paths, nil
}

func untrackedFiles(ctx context.Context, dir string) ([]File, bool, error) {
	out, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, err
	}
	var files []File
	for path := range strings.SplitSeq(out, "\x00") {
		if path == "" {
			continue
		}
		if len(files) == maxUntrackedFiles {
			return files, true, nil
		}
		f, err := countUntracked(filepath.Join(dir, path))
		// Gone since ls-files: a session's temporary file.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		f.Path = path
		files = append(files, f)
	}
	return files, false, nil
}

func countUntracked(path string) (File, error) {
	f := File{Untracked: true}
	// Lstat: a symlink is counted as itself, never followed to a device such as /dev/zero or to a missing target.
	info, err := os.Lstat(path)
	if err != nil {
		return f, fmt.Errorf("stat untracked file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxCountedBytes {
		f.Uncounted = true
		return f, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return f, fmt.Errorf("read untracked file: %w", err)
	}
	if bytes.IndexByte(data[:min(len(data), binarySniffBytes)], 0) >= 0 {
		f.Binary = true
		return f, nil
	}
	f.Added = bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.Added++
	}
	return f, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
