package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanlinitskiy/claude-inbox/internal/action"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
)

const testHome = "/home/user"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadConfigWithoutAFile(t *testing.T) {
	c, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"), testHome)

	require.NoError(t, err)
	assert.Equal(t, fileConfig{TicketPattern: defaultTicketPattern}, c)
}

func TestLoadConfig(t *testing.T) {
	path := writeConfig(t, `{"roots": ["~/work", "/srv/repos"], "workspaces": ["~"], "ide": "code",
		"ticket_pattern": "", "strip_prefix": "acme-", "embed_origin": "http://127.0.0.1:9000"}`)

	c, err := loadConfig(path, testHome)

	require.NoError(t, err)
	assert.Equal(t, fileConfig{
		Roots:       []string{filepath.Join(testHome, "work"), "/srv/repos"},
		Workspaces:  []string{testHome},
		IDE:         "code",
		StripPrefix: "acme-",
		EmbedOrigin: "http://127.0.0.1:9000",
	}, c, "~ expands, and an empty ticket pattern stays empty")
}

func TestLoadConfigExpandsHomeInRepoGroups(t *testing.T) {
	path := writeConfig(t, `{"repo_groups": [{"title": "Work", "repos": [{"repo": "~/work/app"}, {"some": "~/work/{a, b}"}, {"all": "~"}, {"workspace": "~/work"}]}]}`)

	c, err := loadConfig(path, testHome)

	require.NoError(t, err)
	work := filepath.Join(testHome, "work")
	assert.Equal(t, []session.RepoGroup{{Title: "Work", Repos: []session.RepoSource{
		{Dir: work, Names: []string{"app"}},
		{Dir: work, Names: []string{"a", "b"}},
		{Dir: testHome, All: true},
		{Dir: work, Workspace: true},
	}}}, c.RepoGroups)
}

func TestLoadConfigRefusesUnknownFieldsInRepoGroups(t *testing.T) {
	_, err := loadConfig(writeConfig(t, `{"repo_groups": [{"name": "Work", "repos": []}]}`), testHome)

	require.Error(t, err)
}

func TestLoadConfigReadsActions(t *testing.T) {
	path := writeConfig(t, `{"primary_actions": [{"label": "Ticket", "send": "Work on {input}", "input": "Ticket id"}],
		"secondary_actions": [{"label": "Pull", "run": "git pull --ff-only", "confirm": true}]}`)

	c, err := loadConfig(path, testHome)

	require.NoError(t, err)
	assert.Equal(t, []action.Action{{Label: "Ticket", Send: "Work on {input}", Input: "Ticket id"}}, c.PrimaryActions)
	assert.Equal(t, []action.Action{{Label: "Pull", Run: "git pull --ff-only", Confirm: true}}, c.SecondaryActions)
}

func TestLoadConfigRefusesBadActions(t *testing.T) {
	for _, body := range []string{
		`{"primary_actions": [{"label": "Both", "send": "x", "run": "y"}]}`,
		`{"secondary_actions": [{"label": "Ticket", "send": "Work on it", "input": "Ticket id"}]}`,
		`{"secondary_actions": [{"label": "Pull", "command": "git pull"}]}`,
	} {
		_, err := loadConfig(writeConfig(t, body), testHome)

		assert.Error(t, err, body)
	}
}

func TestLoadConfigRefusesUnknownFields(t *testing.T) {
	_, err := loadConfig(writeConfig(t, `{"root": ["~/work"]}`), testHome)

	require.Error(t, err)
}

func TestConfigPath(t *testing.T) {
	xdg := "/tmp/xdg"
	env := map[string]string{}
	getenv := func(key string) string { return env[key] }

	assert.Equal(t, filepath.Join(testHome, ".config", "claude-inbox", "config.json"), configPath(getenv, testHome))
	env["XDG_CONFIG_HOME"] = xdg
	assert.Equal(t, filepath.Join(xdg, "claude-inbox", "config.json"), configPath(getenv, testHome))
}

func TestTicketPattern(t *testing.T) {
	none, err := ticketPattern("")
	require.NoError(t, err)
	assert.Nil(t, none)

	jira, err := ticketPattern(defaultTicketPattern)
	require.NoError(t, err)
	ticket := "PROJ-12"
	assert.Equal(t, ticket, jira.FindString("feature/"+ticket+"-login"))

	_, err = ticketPattern("(")
	require.Error(t, err)
}

func TestEmbedOrigin(t *testing.T) {
	origin := "http://127.0.0.1:7700"
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "none", in: ""},
		{name: "origin", in: origin, want: origin},
		{name: "trailing slash", in: origin + "/", want: origin},
		{name: "https with name", in: "https://home.example", want: "https://home.example"},
		{name: "path", in: origin + "/pages", wantErr: true},
		{name: "query", in: origin + "/?a=1", wantErr: true},
		{name: "no scheme", in: "127.0.0.1:7700", wantErr: true},
		{name: "other scheme", in: "file:///tmp", wantErr: true},
		{name: "wildcard", in: "*", wantErr: true},
		{name: "user info", in: "http://me@127.0.0.1:7700", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := embedOrigin(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitList(t *testing.T) {
	assert.Empty(t, splitList(""))
	assert.Equal(t, []string{"/a", "/b"}, splitList("/a,,/b"))
}
