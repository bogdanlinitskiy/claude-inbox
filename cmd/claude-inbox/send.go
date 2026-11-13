package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/server"
)

// runSend posts text to the running serve, which types it into the session named to, with the sender's name in
// front. The sender is the session this runs in, told by INBOX_SESSION; the link and token come from serve.url.
func runSend(args []string, getenv func(string) string, client *http.Client, stdout io.Writer) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	stateDir := flags.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/claude-inbox or ~/.local/state/claude-inbox)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return errors.New("usage: claude-inbox send <name> <text>")
	}
	to, text := flags.Arg(0), strings.Join(flags.Args()[1:], " ")
	from := getenv(hook.SessionEnv)
	if from == "" {
		return errors.New("only a session claude-inbox started can send; this one has no " + hook.SessionEnv)
	}
	root, err := stateRoot(*stateDir)
	if err != nil {
		return err
	}
	base, token, err := serveLink(filepath.Join(root, urlFile))
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"to": to, "text": text})
	if err != nil {
		return fmt.Errorf("encode the message: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/session/"+url.PathEscape(from)+"/send", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	req.AddCookie(&http.Cookie{Name: server.TokenCookie, Value: token})
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach claude-inbox serve: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only: nothing can be lost when closing
	var reply struct {
		Message  string            `json:"message"`
		Errors   map[string]string `json:"errors"`
		TmuxName string            `json:"tmuxName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return fmt.Errorf("read the answer of claude-inbox serve (%s): %w", resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK {
		for _, e := range reply.Errors {
			reply.Message += " " + e
		}
		return errors.New(reply.Message)
	}
	_, err = fmt.Fprintf(stdout, "Sent to %s.\n", reply.TmuxName)
	return err
}

// serveLink reads the running serve's link and splits it into its origin and its token.
func serveLink(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("claude-inbox serve is not running")
	}
	if err != nil {
		return "", "", fmt.Errorf("read the app link: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("the app link in %s is not a URL", path)
	}
	return u.Scheme + "://" + u.Host, u.Query().Get("token"), nil
}
