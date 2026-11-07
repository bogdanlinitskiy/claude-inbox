// Package settings adds the app's global hook to Claude Code's settings.json, and takes it out again, without
// disturbing the rest of the file: keys keep their order, values the app does not touch keep their exact bytes,
// and the file keeps its indent.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
)

const (
	hooksKey      = "hooks"
	defaultIndent = "  "
	binaryName    = "claude-inbox"
	discoverArgs  = " hook --discover"
	matchAll      = "*"
	commandType   = "command"
	// timeoutSeconds is the hook timeout Claude Code enforces; the hook takes milliseconds.
	timeoutSeconds = 5
)

var ErrNotObject = errors.New("not a JSON object")

type member struct {
	key   string
	value json.RawMessage
}

// Hook is one command in a hooks entry of Claude Code's settings.
type Hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// Matcher is one hooks entry of an event: the hooks to run for the tools it matches.
type Matcher struct {
	Matcher string `json:"matcher"`
	Hooks   []Hook `json:"hooks"`
}

// Entry is the hooks entry that runs command for every tool. The app's own sessions (claude --settings) and the
// global hook register the same one.
func Entry(command string) Matcher {
	return Matcher{Matcher: matchAll, Hooks: []Hook{{Type: commandType, Command: command, Timeout: timeoutSeconds}}}
}

// Quote quotes arg for the shell Claude Code runs hook commands in.
func Quote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// DiscoverCommand is the global hook command for the binary at path.
func DiscoverCommand(path string) string {
	return Quote(path) + discoverArgs
}

// IsDiscoverHook reports whether command runs claude-inbox's global hook, however the binary's path is written.
func IsDiscoverHook(command string) bool {
	words := shellWords(command)
	if len(words) < 3 || words[1] != "hook" || !slices.Contains(words[2:], "--discover") {
		return false
	}
	return filepath.Base(words[0]) == binaryName
}

// shellWords splits command into words as a POSIX shell would, for the quoting hook commands use: single quotes,
// double quotes and backslashes. Nothing is expanded.
func shellWords(command string) []string {
	var words []string
	var word strings.Builder
	inWord := false
	quote := rune(0)
	escaped := false
	for _, r := range command {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quote == '\'' && r == '\'':
			quote = 0
		case quote == '\'':
			word.WriteRune(r)
		case r == '\\' && quote != '\'':
			escaped = true
			inWord = true
		case quote == '"' && r == '"':
			quote = 0
		case quote == '"':
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// Install adds command to every event in hook.Events that has no claude-inbox global hook yet. data unchanged when
// there is nothing to add; empty data counts as an empty file.
func Install(data []byte, command string) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}\n")
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	hooksRaw := lookup(top, hooksKey)
	if hooksRaw == nil {
		hooksRaw = json.RawMessage("{}")
	}
	hooks, err := parseObject(hooksRaw)
	if err != nil {
		return nil, fmt.Errorf("settings %q: %w", hooksKey, err)
	}
	entry, err := json.Marshal(Entry(command))
	if err != nil {
		return nil, fmt.Errorf("encode hook: %w", err)
	}
	changed := false
	for _, event := range hook.Events {
		raw := lookup(hooks, event)
		var entries []json.RawMessage
		if raw != nil {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("settings hooks %q: not a JSON array: %w", event, err)
			}
		}
		if hasDiscoverHook(entries) {
			continue
		}
		hooks = set(hooks, event, encodeArray(append(entries, entry)))
		changed = true
	}
	if !changed {
		return data, nil
	}
	return render(set(top, hooksKey, encodeObject(hooks)), data)
}

// Remove takes every claude-inbox global hook out of the settings: an entry left without hooks goes, and so does
// an event left without entries. data unchanged when there is none.
func Remove(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	hooksRaw := lookup(top, hooksKey)
	if hooksRaw == nil {
		return data, nil
	}
	hooks, err := parseObject(hooksRaw)
	if err != nil {
		return nil, fmt.Errorf("settings %q: %w", hooksKey, err)
	}
	changed := false
	kept := make([]member, 0, len(hooks))
	for _, event := range hooks {
		var entries []json.RawMessage
		if err := json.Unmarshal(event.value, &entries); err != nil {
			kept = append(kept, event)
			continue
		}
		left, removed := withoutDiscoverHooks(entries)
		if !removed {
			kept = append(kept, event)
			continue
		}
		changed = true
		if len(left) > 0 {
			kept = append(kept, member{key: event.key, value: encodeArray(left)})
		}
	}
	if !changed {
		return data, nil
	}
	return render(set(top, hooksKey, encodeObject(kept)), data)
}

func hasDiscoverHook(entries []json.RawMessage) bool {
	for _, raw := range entries {
		var e struct {
			Hooks []Hook `json:"hooks"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		for _, h := range e.Hooks {
			if IsDiscoverHook(h.Command) {
				return true
			}
		}
	}
	return false
}

// withoutDiscoverHooks drops the global hooks from each entry, rewriting only the entries that held one.
func withoutDiscoverHooks(entries []json.RawMessage) ([]json.RawMessage, bool) {
	left := make([]json.RawMessage, 0, len(entries))
	removed := false
	for _, raw := range entries {
		entry, err := parseObject(raw)
		if err != nil {
			left = append(left, raw)
			continue
		}
		var commands []json.RawMessage
		if json.Unmarshal(lookup(entry, hooksKey), &commands) != nil {
			left = append(left, raw)
			continue
		}
		others := make([]json.RawMessage, 0, len(commands))
		for _, c := range commands {
			var h Hook
			if json.Unmarshal(c, &h) == nil && IsDiscoverHook(h.Command) {
				continue
			}
			others = append(others, c)
		}
		if len(others) == len(commands) {
			left = append(left, raw)
			continue
		}
		removed = true
		if len(others) > 0 {
			left = append(left, encodeObject(set(entry, hooksKey, encodeArray(others))))
		}
	}
	return left, removed
}

// parseObject reads a JSON object into its members, in file order, each value as its exact bytes.
func parseObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, ErrNotObject
	}
	var members []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, ErrNotObject
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the JSON object")
	}
	return members, nil
}

func lookup(members []member, key string) json.RawMessage {
	for _, m := range members {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// set replaces key's value in place, or appends key at the end.
func set(members []member, key string, value json.RawMessage) []member {
	for i, m := range members {
		if m.key == key {
			members[i].value = value
			return members
		}
	}
	return append(members, member{key: key, value: value})
}

func encodeObject(members []member) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(encodeString(m.key))
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func encodeArray(values []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(v)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// encodeString writes s as JSON without escaping <, > and &, as Claude Code writes its settings.
func encodeString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// A Go string always encodes.
		panic(err)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// render indents members as original was indented, and ends with a newline if it did.
func render(members []member, original []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, encodeObject(members), "", indentOf(original)); err != nil {
		return nil, fmt.Errorf("indent settings: %w", err)
	}
	if bytes.HasSuffix(original, []byte("\n")) {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// indentOf is the indent of the first indented line, else two spaces.
func indentOf(data []byte) string {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) > 0 && len(trimmed) < len(line) {
			return string(line[:len(line)-len(trimmed)])
		}
	}
	return defaultIndent
}
