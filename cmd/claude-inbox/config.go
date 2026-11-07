package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/action"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
)

// defaultTicketPattern matches Jira-style ids such as PROJ-123.
const defaultTicketPattern = `[A-Z][A-Z0-9]+-\d+`

// fileConfig is the optional config file; a flag of the same name overrides each field.
type fileConfig struct {
	Roots      []string `json:"roots"`
	Workspaces []string `json:"workspaces"`
	// RepoGroups has no flag: it does not fit one.
	RepoGroups []session.RepoGroup `json:"repo_groups"`
	// PrimaryActions and SecondaryActions have no flag either: the bar over the reply box and the Actions menu.
	PrimaryActions   []action.Action `json:"primary_actions"`
	SecondaryActions []action.Action `json:"secondary_actions"`
	IDE              string          `json:"ide"`
	CheatSheet       string          `json:"cheatsheet"`
	TicketPattern    string          `json:"ticket_pattern"`
	StripPrefix      string          `json:"strip_prefix"`
	EmbedOrigin      string          `json:"embed_origin"`
}

func configPath(getenv func(string) string, home string) string {
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "claude-inbox", "config.json")
}

// loadConfig reads the config file; a missing file gives the defaults. Unknown fields are errors, so a misspelt
// key does not pass silently.
func loadConfig(path, home string) (fileConfig, error) {
	c := fileConfig{TicketPattern: defaultTicketPattern}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("read config %s: %w", path, err)
	}
	for i := range c.Roots {
		c.Roots[i] = expandHome(c.Roots[i], home)
	}
	for i := range c.Workspaces {
		c.Workspaces[i] = expandHome(c.Workspaces[i], home)
	}
	for _, group := range c.RepoGroups {
		for i := range group.Repos {
			group.Repos[i].Dir = expandHome(group.Repos[i].Dir, home)
		}
	}
	if err := validateActions("primary_actions", c.PrimaryActions); err != nil {
		return c, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := validateActions("secondary_actions", c.SecondaryActions); err != nil {
		return c, fmt.Errorf("read config %s: %w", path, err)
	}
	return c, nil
}

func validateActions(key string, list []action.Action) error {
	for i, a := range list {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("%s[%d] %q: %w", key, i, a.Label, err)
		}
	}
	return nil
}

// expandHome turns a leading ~ into the home folder: a shell does that for flags, but not inside JSON.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// ticketPattern compiles the configured pattern; empty gives nil, which finds no ticket.
func ticketPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("ticket pattern: %w", err)
	}
	return re, nil
}

// embedOrigin checks that value is a bare origin, scheme://host[:port], as browsers compare it; empty is none.
func embedOrigin(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("embed origin %q: want scheme://host[:port], e.g. http://127.0.0.1:7700", value)
	}
	return u.Scheme + "://" + u.Host, nil
}

// splitList splits a comma-separated flag value without empty items, so "" is no folder at all.
func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' })
}
