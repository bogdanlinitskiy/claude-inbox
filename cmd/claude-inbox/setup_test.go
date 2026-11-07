package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/settings"
)

const (
	testBinary   = "/opt/bin/claude-inbox"
	userSettings = "{\n  \"model\": \"opus\"\n}\n"
)

func setup(t *testing.T, path, answer string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, runSetup(append([]string{"--settings", path}, args...), testBinary, strings.NewReader(answer), &out))
	return out.String()
}

func writeUserSettings(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), settingsFileName)
	require.NoError(t, os.WriteFile(path, []byte(userSettings), 0o644))
	return path
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	found, err := filepath.Glob(path + ".bak-*")
	require.NoError(t, err)
	return found
}

func TestSetupInstallsAfterAYes(t *testing.T) {
	path := writeUserSettings(t)

	out := setup(t, path, "y\n")

	assert.Contains(t, out, "+++ "+path+" (after setup)", "the diff comes first")
	assert.Contains(t, out, settings.DiscoverCommand(testBinary))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "{\n  \"model\": \"opus\",\n  \"hooks\""), "the user's key first, untouched")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the file keeps its mode")
	found := backups(t, path)
	require.Len(t, found, 1)
	backup, err := os.ReadFile(found[0])
	require.NoError(t, err)
	assert.Equal(t, userSettings, string(backup))

	again := setup(t, path, "")
	assert.Contains(t, again, "Nothing to change", "a second run finds the hook")
}

func TestSetupWritesNothingWithoutAYes(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "yess\n"} {
		path := writeUserSettings(t)

		out := setup(t, path, answer)

		assert.Contains(t, out, "Nothing written.", answer)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, userSettings, string(data), answer)
		assert.Empty(t, backups(t, path), answer)
	}
}

func TestSetupYesAndRemove(t *testing.T) {
	path := writeUserSettings(t)
	setup(t, path, "", "--yes")

	out := setup(t, path, "", "--remove", "--yes")

	assert.Contains(t, out, "Wrote ")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), testBinary)
	assert.Contains(t, setup(t, path, "", "--remove"), "Nothing to change")
}

func TestSetupCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", settingsFileName)

	setup(t, path, "", "--yes")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(newSettingsMode), info.Mode().Perm())
	assert.Empty(t, backups(t, path), "nothing to back up")
}

func TestSetupKeepsASymlink(t *testing.T) {
	realPath := writeUserSettings(t)
	link := filepath.Join(t.TempDir(), settingsFileName)
	require.NoError(t, os.Symlink(realPath, link))

	setup(t, link, "", "--yes")

	target, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, realPath, target, "still a link")
	data, err := os.ReadFile(realPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), testBinary, "the linked file got the hook")
}

func TestWriteSettingsRefusesAFileThatChanged(t *testing.T) {
	path := writeUserSettings(t)

	_, err := writeSettings(path, []byte("{}"), []byte("{\"hooks\": {}}"), time.Now())

	require.ErrorIs(t, err, errSettingsChanged)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, userSettings, string(data))
}

func TestWriteSettingsNeverOverwritesABackup(t *testing.T) {
	path := writeUserSettings(t)
	now := time.Now()
	second := "{}\n"
	first, err := writeSettings(path, []byte(userSettings), []byte(second), now)
	require.NoError(t, err)

	next, err := writeSettings(path, []byte(second), []byte(userSettings), now)

	require.NoError(t, err)
	assert.Equal(t, first+"-2", next, "the same second gets the next free name")
	kept, err := os.ReadFile(first)
	require.NoError(t, err)
	assert.Equal(t, userSettings, string(kept))
	newer, err := os.ReadFile(next)
	require.NoError(t, err)
	assert.Equal(t, second, string(newer))
}
