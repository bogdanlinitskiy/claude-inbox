// Package store owns the state directory shared by `serve` and every hook run: session metadata and one
// append-only event file per session.
package store

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	SourceHook    = "hook"
	SourceApp     = "app"
	SourceInvalid = "invalid"

	idLength     = 8
	idAlphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	dirMode      = 0o700
	fileMode     = 0o600
	appDirName   = "claude-inbox"
	sessionsDir  = "sessions"
	eventsDir    = "events"
	archiveDir   = "archive"
	hookSettings = "hook-settings.json"
	metaExt      = ".json"
)

// archiveKeep is how long archived files stay after their last change; only debugging reads them.
const archiveKeep = 30 * 24 * time.Hour

var (
	ErrInvalidID = errors.New("invalid session id")

	idPattern = regexp.MustCompile(`^[a-z0-9]{8}$`)
	// Claude Code's session ids are UUIDs; they name the files of sessions the app did not start.
	claudeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Record is one line of a session's event file: a hook payload as Claude Code sent it, or an event the app
// recorded itself.
type Record struct {
	TS      time.Time       `json:"ts"`
	Source  string          `json:"source"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Raw     string          `json:"raw,omitempty"`
	Event   string          `json:"event,omitempty"`
}

// Meta is what the app knows about a session: one it started, or one the global hook discovered. A discovered
// session has no tmux name; it runs in TmuxPane on the server at TmuxSocket, if in tmux at all, and PID with
// PIDStart identify its claude process (0 when the hook could not find it).
type Meta struct {
	ID         string    `json:"id"`
	Ticket     string    `json:"ticket"`
	Title      string    `json:"title"`
	Repo       string    `json:"repo"`
	Dir        string    `json:"dir"`
	TmuxName   string    `json:"tmuxName"`
	CreatedAt  time.Time `json:"createdAt"`
	Discovered bool      `json:"discovered,omitempty"`
	// Worktree: the app made Dir as a git worktree of the repository Repo for this session.
	Worktree   bool   `json:"worktree,omitempty"`
	PID        int    `json:"pid,omitempty"`
	PIDStart   uint64 `json:"pidStart,omitempty"`
	TmuxSocket string `json:"tmuxSocket,omitempty"`
	TmuxPane   string `json:"tmuxPane,omitempty"`
}

type Dir struct {
	root string
}

func New(root string) Dir {
	return Dir{root: root}
}

// DefaultRoot is $XDG_STATE_HOME/claude-inbox, or ~/.local/state/claude-inbox.
func DefaultRoot() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, appDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", appDirName), nil
}

// ValidID accepts both kinds of session id: the app's own and Claude Code's.
func ValidID(id string) bool {
	return idPattern.MatchString(id) || ValidClaudeID(id)
}

func ValidClaudeID(id string) bool {
	return claudeIDPattern.MatchString(id)
}

func NewID() (string, error) {
	buf := make([]byte, idLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	for i, b := range buf {
		buf[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(buf), nil
}

func (d Dir) Root() string {
	return d.root
}

func (d Dir) HookSettingsPath() string {
	return filepath.Join(d.root, hookSettings)
}

// Init creates the directory layout; it is safe to call on an existing state directory.
func (d Dir) Init() error {
	for _, sub := range []string{sessionsDir, eventsDir, archiveDir} {
		if err := os.MkdirAll(filepath.Join(d.root, sub), dirMode); err != nil {
			return fmt.Errorf("create %s directory: %w", sub, err)
		}
	}
	return nil
}

func (d Dir) EventsPath(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return filepath.Join(d.root, eventsDir, id+".jsonl"), nil
}

func (d Dir) sessionPath(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return filepath.Join(d.root, sessionsDir, id+metaExt), nil
}

// Append adds one record to the session's event file. Hooks of one session can run at the same time, so the
// write happens under an exclusive flock and as a single write call.
func (d Dir) Append(id string, rec Record) error {
	path, err := d.EventsPath(id)
	if err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create events directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("open event file: %w", err)
	}
	writeErr := writeLocked(f, append(line, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close event file: %w", closeErr)
	}
	return nil
}

func writeLocked(f *os.File, line []byte) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock event file: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the file releases the lock anyway
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

func (d Dir) AppendAppEvent(id, event string, now time.Time) error {
	return d.Append(id, Record{TS: now, Source: SourceApp, Event: event})
}

// ReadEvents returns the complete lines after offset and the offset to continue from. A last line without its
// newline is still being written, so it is left for the next read. A line that is not a valid record comes back
// with Source set to SourceInvalid.
func (d Dir) ReadEvents(id string, offset int64) ([]Record, int64, error) {
	path, err := d.EventsPath(id)
	if err != nil {
		return nil, offset, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, fmt.Errorf("open event file: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only: nothing can be lost when closing
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("seek event file: %w", err)
	}

	var records []Record
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return records, offset, nil
		}
		if err != nil {
			return records, offset, fmt.Errorf("read event file: %w", err)
		}
		offset += int64(len(line))
		records = append(records, parseRecord(bytes.TrimSuffix(line, []byte("\n"))))
	}
}

func parseRecord(line []byte) Record {
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil || rec.Source == "" {
		return Record{Source: SourceInvalid, Raw: string(line)}
	}
	return rec
}

// MetaExists reports whether the session has metadata in the active directory.
func (d Dir) MetaExists(id string) (bool, error) {
	path, err := d.sessionPath(id)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check session metadata: %w", err)
	}
	return true, nil
}

// CreateMeta writes the metadata unless the session already has some. Hooks of one session can race on its first
// event, so the whole file is written under a name of this process's own and then linked into place: link fails
// when the name exists, and a reader never sees half a file.
func (d Dir) CreateMeta(m Meta) error {
	path, err := d.sessionPath(m.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create sessions directory: %w", err)
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, fileMode); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	linkErr := os.Link(tmp, path)
	removeErr := os.Remove(tmp)
	if linkErr != nil && !errors.Is(linkErr, os.ErrExist) {
		return fmt.Errorf("store session metadata: %w", linkErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove temporary session metadata: %w", removeErr)
	}
	return nil
}

// SaveMeta writes the metadata through a temporary file and a rename, so a reader never sees half a file.
func (d Dir) SaveMeta(m Meta) error {
	path, err := d.sessionPath(m.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, fileMode); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store session metadata: %w", err)
	}
	return nil
}

// Archive moves a closed session's metadata and events out of the active directories.
func (d Dir) Archive(id string) error {
	meta, err := d.sessionPath(id)
	if err != nil {
		return err
	}
	events, err := d.EventsPath(id)
	if err != nil {
		return err
	}
	for _, path := range []string{meta, events} {
		target := filepath.Join(d.root, archiveDir, filepath.Base(path))
		if err := os.Rename(path, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// PruneArchive deletes the archived files that last changed more than archiveKeep before now.
func (d Dir) PruneArchive(now time.Time) error {
	dir := filepath.Join(d.root, archiveDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list archive: %w", err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat archived %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() || now.Sub(info.ModTime()) <= archiveKeep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prune archived %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// IDs lists the sessions that have metadata in the active directory.
func (d Dir) IDs() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(d.root, sessionsDir, "*"+metaExt))
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	ids := make([]string, 0, len(paths))
	for _, path := range paths {
		if id := strings.TrimSuffix(filepath.Base(path), metaExt); ValidID(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (d Dir) LoadMeta(id string) (Meta, error) {
	path, err := d.sessionPath(id)
	if err != nil {
		return Meta{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return m, nil
}

func (d Dir) LoadMetas() ([]Meta, error) {
	ids, err := d.IDs()
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(ids))
	for _, id := range ids {
		m, err := d.LoadMeta(id)
		if err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	return metas, nil
}
