// Package transcript reads Claude Code's own record of past conversations, <root>/<folder>/<id>.jsonl under
// ~/.claude/projects, so the app can list them and resume one it does not list.
package transcript

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
)

const (
	ext = ".jsonl"
	// The folder and the first prompt are in the first lines; Claude Code repeats the title lines as the
	// conversation goes on, so the last ones are near the end. Reading only both ends keeps a listing of hundreds
	// of transcripts, some of them tens of MB, fast.
	headBytes      = 64 << 10
	tailBytes      = 256 << 10
	maxPromptRunes = 200
	// A preview reads further back from the end: the last prompts can sit behind long tool output.
	previewTailBytes = 4 << 20
	previewPrompts   = 3
	maxPreviewRunes  = 600
	maxReplyRunes    = 2000

	typeUser        = "user"
	typeAssistant   = "assistant"
	typeTextBlock   = "text"
	aiTitleMarker   = `"type":"ai-title"`
	customTitleMark = `"type":"custom-title"`
	userMarker      = `"type":"user"`
	assistantMarker = `"type":"assistant"`
	// Command runs, caveats and command output come as user messages wrapped in a tag, not as prompts.
	tagStart = "<"
)

var (
	ErrNotFound = errors.New("conversation not found")

	commandName = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	commandArgs = regexp.MustCompile(`(?s)<command-args>([^<]*)</command-args>`)
)

// Preview is what a conversation was about.
type Preview struct {
	FirstPrompt string   `json:"firstPrompt"`
	LastPrompts []string `json:"lastPrompts"`
	LastReply   string   `json:"lastReply"`
}

// Conversation is one past conversation: its folder and branch as it started, its title and its first prompt.
type Conversation struct {
	ID        string
	Title     string
	Prompt    string
	Dir       string
	Branch    string
	UpdatedAt time.Time
}

type line struct {
	Type        string `json:"type"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// List returns every conversation under root, latest first. A transcript with no folder recorded, or without a
// single reply from Claude (only /clear or /exit was run), holds no conversation and is left out.
func List(root string) ([]Conversation, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*"+ext))
	if err != nil {
		return nil, fmt.Errorf("list transcripts: %w", err)
	}
	list := make([]Conversation, 0, len(paths))
	for _, path := range paths {
		c, ok, err := read(path)
		if err != nil {
			return nil, err
		}
		if ok {
			list = append(list, c)
		}
	}
	slices.SortFunc(list, func(a, b Conversation) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return list, nil
}

// Find returns the conversation with the given id.
func Find(root, id string) (Conversation, error) {
	path, err := pathOf(root, id)
	if err != nil {
		return Conversation{}, err
	}
	c, ok, err := read(path)
	if err != nil {
		return Conversation{}, err
	}
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

// ReadPreview returns what the conversation with the given id was about: the first prompt, the last few, and
// Claude's last reply. Prompts and the reply older than the last previewTailBytes are not looked for.
func ReadPreview(root, id string) (Preview, error) {
	path, err := pathOf(root, id)
	if err != nil {
		return Preview{}, err
	}
	head, tail, _, err := window(path, previewTailBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Preview{}, ErrNotFound
	}
	if err != nil {
		return Preview{}, err
	}
	p := Preview{LastPrompts: []string{}}
	for l := range lines(head) {
		if text, _ := typed(l); text != "" {
			p.FirstPrompt = cut(text, maxPreviewRunes)
			break
		}
	}
	var prompts []string
	for _, raw := range bytes.Split(tail, []byte("\n")) {
		if !bytes.Contains(raw, []byte(userMarker)) && !bytes.Contains(raw, []byte(assistantMarker)) {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if text, _ := typed(l); text != "" {
			prompts = append(prompts, cut(text, maxPreviewRunes))
		}
		if l.Type == typeAssistant {
			if text := strings.TrimSpace(textOf(l.Message.Content)); text != "" {
				p.LastReply = cut(text, maxReplyRunes)
			}
		}
	}
	p.LastPrompts = append(p.LastPrompts, prompts[max(0, len(prompts)-previewPrompts):]...)
	return p, nil
}

func pathOf(root, id string) (string, error) {
	if !store.ValidClaudeID(id) {
		return "", ErrNotFound
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", id+ext))
	if err != nil {
		return "", fmt.Errorf("find transcript: %w", err)
	}
	if len(paths) == 0 {
		return "", ErrNotFound
	}
	return paths[0], nil
}

func read(path string) (Conversation, bool, error) {
	id := strings.TrimSuffix(filepath.Base(path), ext)
	if !store.ValidClaudeID(id) {
		return Conversation{}, false, nil
	}
	head, tail, modTime, err := window(path, tailBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, err
	}

	c := Conversation{ID: id, UpdatedAt: modTime}
	var prompt string
	for l := range lines(head) {
		if c.Dir == "" && l.Cwd != "" {
			c.Dir, c.Branch = l.Cwd, l.GitBranch
		}
		// A command such as /clear says little about the conversation, so it never becomes its title.
		if text, command := typed(l); prompt == "" && text != "" && !command {
			prompt = cut(strings.Join(strings.Fields(text), " "), maxPromptRunes)
		}
		if c.Dir != "" && prompt != "" {
			break
		}
	}
	if c.Dir == "" || !bytes.Contains(head, []byte(assistantMarker)) && !bytes.Contains(tail, []byte(assistantMarker)) {
		return Conversation{}, false, nil
	}
	var aiTitle, customTitle string
	for _, raw := range bytes.Split(tail, []byte("\n")) {
		if !bytes.Contains(raw, []byte(aiTitleMarker)) && !bytes.Contains(raw, []byte(customTitleMark)) {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if l.CustomTitle != "" {
			customTitle = l.CustomTitle
		}
		if l.AITitle != "" {
			aiTitle = l.AITitle
		}
	}
	c.Prompt = prompt
	c.Title = cmp.Or(customTitle, aiTitle, prompt)
	return c, true, nil
}

// window reads the first headBytes and the last tailN bytes of the transcript at path, and when it last changed.
func window(path string, tailN int64) (head, tail []byte, modTime time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("stat transcript: %w", err)
	}
	if head, err = readAt(f, 0, min(info.Size(), headBytes)); err != nil {
		return nil, nil, time.Time{}, err
	}
	tailStart := max(0, info.Size()-tailN)
	if tail, err = readAt(f, tailStart, info.Size()-tailStart); err != nil {
		return nil, nil, time.Time{}, err
	}
	return head, tail, info.ModTime(), nil
}

func readAt(f *os.File, offset, n int64) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	return buf, nil
}

// lines decodes the whole lines of buf as the caller ranges over them; a window can cut its first and last line
// short.
func lines(buf []byte) iter.Seq[line] {
	return func(yield func(line) bool) {
		for raw := range bytes.SplitSeq(buf, []byte("\n")) {
			var l line
			if json.Unmarshal(raw, &l) == nil && !yield(l) {
				return
			}
		}
	}
}

// typed returns what the user typed in l: a prompt, or a command with its arguments (command is true). It is ""
// for anything else a user line carries: tool results, caveats, command output, text the app or a skill added.
func typed(l line) (text string, command bool) {
	if l.Type != typeUser || l.IsMeta {
		return "", false
	}
	text = strings.TrimSpace(textOf(l.Message.Content))
	if !strings.HasPrefix(text, tagStart) {
		return text, false
	}
	name := commandName.FindStringSubmatch(text)
	if name == nil {
		return "", false
	}
	args := commandArgs.FindStringSubmatch(text)
	if args == nil {
		return name[1], true
	}
	return strings.TrimSpace(name[1] + " " + strings.TrimSpace(args[1])), true
}

// textOf returns a message's text: the content itself, or its first text block.
func textOf(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == typeTextBlock {
			return b.Text
		}
	}
	return ""
}

func cut(text string, maxRunes int) string {
	if runes := []rune(text); len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
}
