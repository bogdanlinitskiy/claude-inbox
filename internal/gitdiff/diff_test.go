package gitdiff

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// newRepo makes a repository whose first commit is on branch.
func newRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "--initial-branch", branch)
	write(t, dir, "README.md", "line 1\nline 2\nline 3\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "--quiet", "-m", "base")
	return dir
}

func byPath(files []File) map[string]File {
	m := make(map[string]File, len(files))
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func TestComputeOnFeatureBranchOffMaster(t *testing.T) {
	dir := newRepo(t, "master")
	run(t, dir, "switch", "--quiet", "-c", "PROJ-1600")
	write(t, dir, "README.md", "line 1\nline two\nline 3\nline 4\n")
	write(t, dir, "src/committed.go", "package src\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "--quiet", "-m", "work")
	write(t, dir, "src/committed.go", "package src\n\nfunc A() {}\n")
	write(t, dir, "src/new.go", "package src\n\nfunc B() {}")
	write(t, dir, "image.bin", "\x89PNG\x00\x01\x02")

	d, err := Compute(context.Background(), dir)

	require.NoError(t, err)
	assert.Equal(t, "master", d.Base)
	assert.False(t, d.Truncated)
	files := byPath(d.Files)
	require.Len(t, files, 4)
	assert.Equal(t, File{Path: "README.md", Added: 2, Deleted: 1}, files["README.md"])
	assert.Equal(t, File{Path: "src/committed.go", Added: 3, Deleted: 0}, files["src/committed.go"], "committed and uncommitted changes together")
	assert.Equal(t, File{Path: "src/new.go", Added: 3, Untracked: true}, files["src/new.go"], "last line without a newline still counts")
	assert.Equal(t, File{Path: "image.bin", Binary: true, Untracked: true}, files["image.bin"])
	assert.Contains(t, d.Patch, "+line two")
	assert.Contains(t, d.Patch, "+func A() {}")
}

func TestDefaultBase(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		base, err := DefaultBase(context.Background(), newRepo(t, "main"))
		require.NoError(t, err)
		assert.Equal(t, "main", base)
	})

	t.Run("master wins over main", func(t *testing.T) {
		dir := newRepo(t, "main")
		run(t, dir, "branch", "master")
		base, err := DefaultBase(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, "master", base)
	})

	t.Run("origin/HEAD wins", func(t *testing.T) {
		origin := newRepo(t, "trunk")
		clone := filepath.Join(t.TempDir(), "clone")
		run(t, origin, "clone", "--quiet", origin, clone)
		base, err := DefaultBase(context.Background(), clone)
		require.NoError(t, err)
		assert.Equal(t, "origin/trunk", base)
	})

	t.Run("no default branch", func(t *testing.T) {
		_, err := DefaultBase(context.Background(), newRepo(t, "develop"))
		require.ErrorIs(t, err, ErrNoBase)
	})
}

func TestCurrentBranch(t *testing.T) {
	dir := newRepo(t, "master")
	branch := "PROJ-392-PROJ-1128"
	run(t, dir, "switch", "--quiet", "-c", branch)

	got, err := CurrentBranch(context.Background(), dir)

	require.NoError(t, err)
	assert.Equal(t, branch, got)
}

func TestComputeWithoutChanges(t *testing.T) {
	d, err := Compute(context.Background(), newRepo(t, "master"))

	require.NoError(t, err)
	assert.Empty(t, d.Files)
	assert.Empty(t, d.Patch)
	encoded, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"files":[]`, "the page expects an array, never null")
}

func TestComputeCutsLargePatch(t *testing.T) {
	dir := newRepo(t, "master")
	write(t, dir, "README.md", strings.Repeat("a long changed line\n", maxPatchBytes/10))

	d, err := Compute(context.Background(), dir)

	require.NoError(t, err)
	assert.True(t, d.Truncated)
	assert.Len(t, d.Patch, maxPatchBytes)
}

// A symlink is not followed: a dangling one used to fail the whole diff, one to /dev/zero to read forever.
func TestComputeDoesNotFollowUntrackedSymlinks(t *testing.T) {
	dir := newRepo(t, "master")
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling")))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(dir, "zero")))

	d, err := Compute(context.Background(), dir)

	require.NoError(t, err)
	files := byPath(d.Files)
	for _, name := range []string{"dangling", "zero"} {
		assert.True(t, files[name].Untracked, name)
		assert.True(t, files[name].Uncounted, name)
	}
}

func TestComputeOutsideGit(t *testing.T) {
	_, err := Compute(context.Background(), t.TempDir())
	require.Error(t, err)
}

func TestParseNumstat(t *testing.T) {
	files, err := parseNumstat("3\t1\ta.go\x00-\t-\tlogo.png\x0010\t0\tdir with space/b.go\x00")

	require.NoError(t, err)
	assert.Equal(t, []File{
		{Path: "a.go", Added: 3, Deleted: 1},
		{Path: "logo.png", Binary: true},
		{Path: "dir with space/b.go", Added: 10},
	}, files)
}

func TestParseNumstatRejectsGarbage(t *testing.T) {
	_, err := parseNumstat("nonsense\x00")
	require.Error(t, err)
}

func TestAddWorktree(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "master")
	run(t, repo, "branch", "PROJ-2")

	fresh := filepath.Join(t.TempDir(), "fresh")
	require.NoError(t, AddWorktree(ctx, repo, fresh, "PROJ-1-part-2"))
	branch, err := CurrentBranch(ctx, fresh)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1-part-2", branch, "a new branch from the default branch")

	existing := filepath.Join(t.TempDir(), "existing")
	require.NoError(t, AddWorktree(ctx, repo, existing, "PROJ-2"))
	branch, err = CurrentBranch(ctx, existing)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-2", branch, "an existing branch as it is")

	err = AddWorktree(ctx, repo, filepath.Join(t.TempDir(), "taken"), "master")
	require.Error(t, err, "master is checked out in the repository itself")

	for _, bad := range []string{"-x", "a..b", "with space"} {
		require.ErrorIs(t, AddWorktree(ctx, repo, filepath.Join(t.TempDir(), "bad"), bad), ErrBadBranch, bad)
	}
}
