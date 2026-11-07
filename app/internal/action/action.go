// Package action is the config's buttons: a prompt typed into a session, or a shell command run in its folder.
package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// InputPlaceholder in a send action's text is replaced by what the user typed.
	InputPlaceholder = "{input}"
	// InputEnv carries what the user typed to a run action's command; never spliced into the command itself.
	InputEnv = "INBOX_INPUT"
	// Timeout stops a run action's command and everything it started.
	Timeout = 5 * time.Minute
	// OutputLimit is how much of the end of a command's output is kept.
	OutputLimit = 16 << 10
	// waitDelay: a child left holding the output pipe after the kill does not keep the request open.
	waitDelay = 5 * time.Second
)

// Action is one entry of primary_actions or secondary_actions: exactly one of Send and Run.
type Action struct {
	Label string `json:"label"`
	// Send is typed into the session as a prompt; InputPlaceholder in it is replaced by the input.
	Send string `json:"send,omitempty"`
	// Run is a sh command run in the session's folder; the input is in $INBOX_INPUT.
	Run string `json:"run,omitempty"`
	// Input, when set, is the label of the one-line field the page asks for before acting.
	Input string `json:"input,omitempty"`
	// Confirm makes the page ask for a second click before acting.
	Confirm bool `json:"confirm,omitempty"`
}

// Validate refuses an entry that cannot work as written.
func (a Action) Validate() error {
	switch {
	case strings.TrimSpace(a.Label) == "":
		return errors.New("label is empty")
	case (a.Send == "") == (a.Run == ""):
		return errors.New("set exactly one of send and run")
	case a.Run != "" && strings.Contains(a.Run, InputPlaceholder):
		return fmt.Errorf("run does not replace %s; read $%s instead", InputPlaceholder, InputEnv)
	case a.Send != "" && (a.Input != "") != strings.Contains(a.Send, InputPlaceholder):
		return fmt.Errorf("send needs %s exactly when input is set", InputPlaceholder)
	}
	return nil
}

// Text is the prompt a send action types, with the input in place.
func (a Action) Text(input string) string {
	return strings.ReplaceAll(a.Send, InputPlaceholder, input)
}

// Result is how a run action's command ended.
type Result struct {
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	// Truncated: Output is only the last OutputLimit bytes.
	Truncated bool `json:"truncated,omitempty"`
	TimedOut  bool `json:"timedOut,omitempty"`
}

// Run runs command with sh in dir and waits for it. A command that fails is a Result with its exit code; the error
// is only for a command that could not start.
func Run(ctx context.Context, dir, command, input string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), InputEnv+"="+input)
	out := &tail{limit: OutputLimit}
	cmd.Stdout = out
	cmd.Stderr = out
	// Its own process group, so the kill reaches what the command started (make, go test …), not only sh.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	res := Result{Output: string(out.buf), Truncated: out.cut, TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
	default:
		return Result{}, fmt.Errorf("run %q: %w", command, err)
	}
	return res, nil
}

// tail keeps the last limit bytes written to it.
type tail struct {
	buf   []byte
	limit int
	cut   bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.cut = true
	}
	return len(p), nil
}
