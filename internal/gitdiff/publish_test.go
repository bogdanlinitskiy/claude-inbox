package gitdiff

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitIdentity lets Commit run where no git identity is configured, as in the test container.
func commitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

// newClone makes a bare origin on master and a clone of it on a new branch.
func newClone(t *testing.T, branch string) (origin, clone string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), "origin.git")
	run(t, newRepo(t, "master"), "clone", "--quiet", "--bare", ".", origin)
	clone = filepath.Join(t.TempDir(), "clone")
	run(t, origin, "clone", "--quiet", origin, clone)
	run(t, clone, "switch", "--quiet", "-c", branch)
	return origin, clone
}

func TestComputeListsUncommittedPaths(t *testing.T) {
	dir := newRepo(t, "master")
	write(t, dir, "src/kept.go", "package src\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "--quiet", "-m", "kept")
	write(t, dir, "README.md", "changed\n")
	write(t, dir, "src/new file.go", "package src\n")
	run(t, dir, "rm", "--quiet", "src/kept.go")

	d, err := Compute(context.Background(), dir)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"README.md", "src/new file.go", "src/kept.go"}, d.Uncommitted)
}

func TestCommitTakesOnlyTheGivenPaths(t *testing.T) {
	commitIdentity(t)
	dir := newRepo(t, "master")
	write(t, dir, "README.md", "changed\n")
	write(t, dir, "src/new.go", "package src\n")
	write(t, dir, "left-out.txt", "not this one\n")
	message := "PROJ-1 Commit from the Diff tab"

	out, err := Commit(context.Background(), dir, message, []string{"README.md", "src/new.go"})

	require.NoError(t, err)
	assert.Contains(t, out, message)
	assert.Equal(t, message, gitOut(t, dir, "log", "-1", "--format=%s"))
	assert.ElementsMatch(t, []string{"README.md", "src/new.go"}, strings.Fields(gitOut(t, dir, "show", "--name-only", "--format=", "HEAD")))
	assert.Equal(t, "?? left-out.txt", gitOut(t, dir, "status", "--porcelain"), "the other change stays uncommitted")
}

func TestCommitTakesPathsLiterally(t *testing.T) {
	commitIdentity(t)
	dir := newRepo(t, "master")
	write(t, dir, "a.txt", "a\n")
	write(t, dir, "b.txt", "b\n")

	_, err := Commit(context.Background(), dir, "glob", []string{"*.txt"})

	require.Error(t, err, "no file is named *.txt")
	assert.Equal(t, "base", gitOut(t, dir, "log", "-1", "--format=%s"))
}

func TestCommitWithNothingChanged(t *testing.T) {
	commitIdentity(t)
	dir := newRepo(t, "master")

	_, err := Commit(context.Background(), dir, "empty", []string{"README.md"})

	require.Error(t, err)
}

func TestPushSetsTheUpstream(t *testing.T) {
	commitIdentity(t)
	branch := "PROJ-1-push"
	origin, clone := newClone(t, branch)
	write(t, clone, "README.md", "changed\n")
	_, err := Commit(context.Background(), clone, "work", []string{"README.md"})
	require.NoError(t, err)

	_, err = Push(context.Background(), clone)

	require.NoError(t, err)
	assert.Equal(t, gitOut(t, clone, "rev-parse", "HEAD"), gitOut(t, origin, "rev-parse", branch))
	assert.Equal(t, remote+"/"+branch, gitOut(t, clone, "rev-parse", "--abbrev-ref", "@{upstream}"))
}

func TestPushRefusesTheDefaultBranchAndADetachedHead(t *testing.T) {
	_, clone := newClone(t, "PROJ-1")
	run(t, clone, "switch", "--quiet", "master")

	_, onDefault := Push(context.Background(), clone)
	_, prOnDefault := OpenPR(context.Background(), clone)
	run(t, clone, "switch", "--quiet", "--detach")
	_, detached := Push(context.Background(), clone)

	require.ErrorIs(t, onDefault, ErrDefaultBranch)
	require.ErrorIs(t, prOnDefault, ErrDefaultBranch, "refused before gh runs")
	require.ErrorIs(t, detached, ErrDetached)
}
