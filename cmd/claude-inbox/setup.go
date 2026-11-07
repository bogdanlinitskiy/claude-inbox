package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/settings"
)

const (
	backupLayout     = "2006-01-02T150405"
	newSettingsMode  = 0o600
	settingsDirMode  = 0o700
	diffExitChanged  = 1
	confirmQuestion  = "Write this to %s? [y/N] "
	settingsFileName = "settings.json"
)

var errSettingsChanged = errors.New("the settings file changed while you were looking at the diff; run setup again")

// runSetup adds the global hook for the binary at self to Claude Code's settings (or, with --remove, takes it out),
// after showing the exact change and asking.
func runSetup(args []string, self string, stdin io.Reader, stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	remove := flags.Bool("remove", false, "take the hook out instead")
	yes := flags.Bool("yes", false, "write without asking")
	path := flags.String("settings", filepath.Join(home, ".claude", settingsFileName), "Claude Code settings file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	target, err := resolveSettings(*path)
	if err != nil {
		return err
	}
	old, err := readSettings(target)
	if err != nil {
		return err
	}
	var updated []byte
	if *remove {
		updated, err = settings.Remove(old)
	} else {
		updated, err = settings.Install(old, settings.DiscoverCommand(self))
	}
	if err != nil {
		return err
	}
	if bytes.Equal(old, updated) {
		state := "already in"
		if *remove {
			state = "not in"
		}
		_, err := fmt.Fprintf(stdout, "Nothing to change: the claude-inbox hook is %s %s.\n", state, *path)
		return err
	}
	if err := printDiff(stdout, *path, old, updated); err != nil {
		return err
	}
	if !*yes {
		ok, err := confirm(stdin, stdout, fmt.Sprintf(confirmQuestion, *path))
		if err != nil || !ok {
			if err == nil {
				_, err = fmt.Fprintln(stdout, "Nothing written.")
			}
			return err
		}
	}
	backup, err := writeSettings(target, old, updated, time.Now())
	if err != nil {
		return err
	}
	if backup != "" {
		_, err = fmt.Fprintf(stdout, "Wrote %s; the old version is in %s. Running sessions pick it up on their next event.\n", *path, backup)
	} else {
		_, err = fmt.Fprintf(stdout, "Wrote %s. Running sessions pick it up on their next event.\n", *path)
	}
	return err
}

// resolveSettings follows a symlinked settings file (dotfile managers keep one), so the link stays a link.
func resolveSettings(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return target, nil
}

// readSettings returns the file's bytes; a missing file reads as empty.
func readSettings(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return data, nil
}

// writeSettings replaces the file at path with updated, unless it no longer holds old. It keeps old as a backup
// next to it first, and writes through a temporary file and a rename, so a crash leaves either version whole.
func writeSettings(path string, old, updated []byte, now time.Time) (string, error) {
	current, err := readSettings(path)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(current, old) {
		return "", errSettingsChanged
	}
	mode := fs.FileMode(newSettingsMode)
	backup := ""
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if backup, err = writeBackup(path+".bak-"+now.Format(backupLayout), old, mode); err != nil {
			return "", fmt.Errorf("back up settings: %w", err)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, settingsDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return "", fmt.Errorf("write settings: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("write settings: %w", err)
	}
	return backup, nil
}

// writeBackup writes data to base, or to base-2, base-3… when an earlier backup has that name, and never
// overwrites one.
func writeBackup(base string, data []byte, mode fs.FileMode) (string, error) {
	for n := 1; ; n++ {
		path := base
		if n > 1 {
			path = fmt.Sprintf("%s-%d", base, n)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return "", err
		}
		return path, f.Close()
	}
}

// printDiff shows the change as diff -u does; without diff on PATH, the whole new file.
func printDiff(w io.Writer, name string, old, updated []byte) error {
	if _, err := exec.LookPath("diff"); err != nil {
		_, err := fmt.Fprintf(w, "%s would become:\n%s", name, updated)
		return err
	}
	dir, err := os.MkdirTemp("", "claude-inbox-setup-")
	if err != nil {
		return fmt.Errorf("prepare diff: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	before, after := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	if err := os.WriteFile(before, old, newSettingsMode); err != nil {
		return fmt.Errorf("prepare diff: %w", err)
	}
	if err := os.WriteFile(after, updated, newSettingsMode); err != nil {
		return fmt.Errorf("prepare diff: %w", err)
	}
	cmd := exec.Command("diff", "-u", "--label", name, "--label", name+" (after setup)", before, after)
	cmd.Stdout = w
	var exitErr *exec.ExitError
	if err := cmd.Run(); err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != diffExitChanged) {
		return fmt.Errorf("show diff: %w", err)
	}
	return nil
}

// confirm asks question and reads one line; only y or yes agrees, and no answer at all (EOF) is a no.
func confirm(stdin io.Reader, stdout io.Writer, question string) (bool, error) {
	if _, err := fmt.Fprint(stdout, question); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read answer: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
