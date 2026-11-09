// Package tmux runs the tmux commands the app needs to start, drive and watch Claude sessions.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	exitSessionNotFound = 1
	pasteBufferPrefix   = "claude-inbox-"
)

var (
	ErrInvalidName = errors.New("invalid tmux session name")

	// tmux turns '.' and ':' in session names into '_', and names are used unquoted in targets.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// A pane id such as %12 names one pane on its server; sessions the app did not start are reached by it.
	paneIDPattern = regexp.MustCompile(`^%\d+$`)
)

// Client talks to one tmux server: the default one, a named socket (tmux -L) so tests stay off the user's own
// sessions, or the socket at a path (tmux -S) that a session someone else started runs on.
type Client struct {
	socket     string
	socketPath string
}

func New(socket string) Client {
	return Client{socket: socket}
}

func NewAt(socketPath string) Client {
	return Client{socketPath: socketPath}
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

type NewSession struct {
	Name    string
	Dir     string
	Env     map[string]string
	Width   int
	Height  int
	Command []string
}

// Start creates a detached session running opts.Command in opts.Dir, with opts.Env added to its environment.
func (c Client) Start(ctx context.Context, opts NewSession) error {
	if !ValidName(opts.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, opts.Name)
	}
	args := []string{"new-session", "-d", "-s", opts.Name, "-c", opts.Dir,
		"-x", strconv.Itoa(opts.Width), "-y", strconv.Itoa(opts.Height)}
	for key, value := range opts.Env {
		args = append(args, "-e", key+"="+value)
	}
	args = append(args, "--")
	args = append(args, opts.Command...)
	_, err := c.run(ctx, args...)
	return err
}

// SetOption sets a session option. set-option resolves -t as a pane, so it takes the pane form of the target.
func (c Client) SetOption(ctx context.Context, name, option, value string) error {
	_, err := c.run(ctx, "set-option", "-t", paneTarget(name), option, value)
	return err
}

// SendKeys sends tmux key names such as "Enter", "Escape", "Down" or "C-c".
func (c Client) SendKeys(ctx context.Context, name string, keys ...string) error {
	args := append([]string{"send-keys", "-t", paneTarget(name)}, keys...)
	_, err := c.run(ctx, args...)
	return err
}

// SendText types text literally, without interpreting key names.
func (c Client) SendText(ctx context.Context, name, text string) error {
	_, err := c.run(ctx, "send-keys", "-t", paneTarget(name), "-l", "--", text)
	return err
}

// PasteText pastes text as one bracketed paste, so an application that asked for bracketed paste takes its line
// breaks as text rather than as Enter. The text goes through stdin because tmux limits the size of a command.
func (c Client) PasteText(ctx context.Context, name, text string) error {
	buffer := pasteBufferPrefix + strings.TrimPrefix(name, "%")
	if _, err := c.runWithInput(ctx, text, "load-buffer", "-b", buffer, "-"); err != nil {
		return err
	}
	_, err := c.run(ctx, "paste-buffer", "-p", "-d", "-b", buffer, "-t", paneTarget(name))
	return err
}

// Capture returns the visible screen of the session's active pane as plain text.
func (c Client) Capture(ctx context.Context, name string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-t", paneTarget(name))
}

func (c Client) Has(ctx context.Context, name string) (bool, error) {
	_, err := c.run(ctx, "has-session", "-t", sessionTarget(name))
	if err == nil {
		return true, nil
	}
	// has-session exits 1 both for a missing session and when no tmux server runs: either way it is not there.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == exitSessionNotFound {
		return false, nil
	}
	return false, err
}

func (c Client) Kill(ctx context.Context, name string) error {
	_, err := c.run(ctx, "kill-session", "-t", sessionTarget(name))
	return err
}

// AttachCommand is the command line that attaches a terminal to the session.
func (c Client) AttachCommand(name string) []string {
	return append(c.base(), "attach-session", "-t", sessionTarget(name))
}

// The "=" prefix makes tmux match the session name exactly instead of by prefix, so "PROJ-1-wallet" never
// resolves to "PROJ-1-wallet-2". A pane id is exact already and also stands for the session that holds the pane.
func sessionTarget(name string) string {
	if paneIDPattern.MatchString(name) {
		return name
	}
	return "=" + name
}

func paneTarget(name string) string {
	if paneIDPattern.MatchString(name) {
		return name
	}
	return "=" + name + ":"
}

func (c Client) base() []string {
	switch {
	case c.socketPath != "":
		return []string{"tmux", "-S", c.socketPath}
	case c.socket != "":
		return []string{"tmux", "-L", c.socket}
	default:
		return []string{"tmux"}
	}
}

func (c Client) run(ctx context.Context, args ...string) (string, error) {
	return c.runWithInput(ctx, "", args...)
}

func (c Client) runWithInput(ctx context.Context, input string, args ...string) (string, error) {
	base := c.base()
	cmd := exec.CommandContext(ctx, base[0], append(base[1:], args...)...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
