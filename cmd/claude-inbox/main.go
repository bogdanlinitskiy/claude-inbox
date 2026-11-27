package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bogdanlinitskiy/claude-inbox/internal/gitdiff"
	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/proc"
	"github.com/bogdanlinitskiy/claude-inbox/internal/server"
	"github.com/bogdanlinitskiy/claude-inbox/internal/session"
	"github.com/bogdanlinitskiy/claude-inbox/internal/store"
	"github.com/bogdanlinitskiy/claude-inbox/internal/tmux"
	"github.com/bogdanlinitskiy/claude-inbox/web"
)

const usage = `usage: claude-inbox <command> [flags]

commands:
  serve      run the app (claude-inbox serve -h for flags)
  hook       record a Claude Code hook payload (run by Claude Code, not by hand)
  setup      add the global hook to ~/.claude/settings.json, so sessions started elsewhere are listed too
             (setup --remove takes it out; claude-inbox setup -h for flags)
  tmux-name  print the tmux session name for a session in the current folder (used by claude-tmux)
  send       message another session of the same group: claude-inbox send <name> <text> (run by a session)
  version    print the version (also --version)
`

const (
	defaultPort         = 7788
	trustWait           = 3 * time.Second
	deliveryTimeout     = 3 * time.Second
	keyPause            = 300 * time.Millisecond
	pollInterval        = 200 * time.Millisecond
	// discoverInterval only has to notice a new session file; the events of the sessions already listed are
	// what pollInterval is for.
	discoverInterval = time.Second
	paneCheckInterval   = 5 * time.Second
	screenCheckInterval = 2 * time.Second
	// stallAfter: a working session of a group silent this long may have lost its connection, which no hook reports.
	stallAfter = 5 * time.Minute
	readHeaderTimeout   = 10 * time.Second
	shutdownTimeout     = 5 * time.Second
	// procRoot is where Linux keeps its process table; macOS has none and reads ps instead.
	procRoot          = "/proc"
	claudeCommand     = "claude"
	claudeSearchDepth = 5
	// urlFile in the state directory holds the link of the running serve, token included, for local tools that
	// open the app without the printed link.
	urlFile     = "serve.url"
	urlFileMode = 0o600
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "claude-inbox serve:", err)
			os.Exit(1)
		}
	case "setup":
		self, err := executable()
		if err == nil {
			err = runSetup(os.Args[2:], self, os.Stdin, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "claude-inbox setup:", err)
			os.Exit(1)
		}
	case "version", "--version":
		info, ok := debug.ReadBuildInfo()
		fmt.Println("claude-inbox", versionString(version, info, ok))
	case "send":
		if err := runSend(os.Args[2:], os.Getenv, http.DefaultClient, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "claude-inbox send:", err)
			os.Exit(1)
		}
	case "tmux-name":
		if err := runTmuxName(); err != nil {
			fmt.Fprintln(os.Stderr, "claude-inbox tmux-name:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// runHook always exits 0 and never prints to stdout: a failing or chatty hook would change what the Claude
// session does. Errors go to stderr, which Claude Code only shows in verbose mode.
func runHook(args []string) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	flags := flag.NewFlagSet("hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/claude-inbox or ~/.local/state/claude-inbox)")
	discover := flags.Bool("discover", false, "record any session the app did not start (the global hook)")
	if err := flags.Parse(args); err != nil {
		logger.Error("claude-inbox hook: parse flags", "err", err)
		return
	}
	root, err := stateRoot(*stateDir)
	if err != nil {
		logger.Error("claude-inbox hook: find state directory", "err", err)
		return
	}
	dir := store.New(root)
	if *discover {
		err = hook.Discover(dir, os.Getenv, os.Stdin, time.Now(), findClaude)
	} else {
		err = hook.Run(dir, os.Getenv, os.Stdin, time.Now())
	}
	if err != nil {
		logger.Error("claude-inbox hook", "session", os.Getenv(hook.SessionEnv), "discover", *discover, "err", err)
	}
}

// findClaude looks for the claude process among the hook's ancestors; Claude Code may start the hook through a
// shell.
func findClaude() (proc.Process, bool) {
	return proc.New(procRoot).FindAncestor(os.Getppid(), claudeCommand, claudeSearchDepth)
}

type serveFlags struct {
	port          int
	stateDir      string
	roots         string
	workspaces    string
	claude        string
	ide           string
	ticketPattern string
	stripPrefix   string
	embedOrigin   string
	open          bool
}

func runServe(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	cfgPath := configPath(os.Getenv, home)
	cfg, err := loadConfig(cfgPath, home)
	if err != nil {
		return err
	}
	var f serveFlags
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.Usage = func() {
		if _, err := fmt.Fprintf(flags.Output(), "usage: claude-inbox serve [flags]\n\nDefaults come from %s when it exists.\n\n", cfgPath); err != nil {
			return
		}
		flags.PrintDefaults()
	}
	flags.IntVar(&f.port, "port", defaultPort, "port on 127.0.0.1")
	flags.StringVar(&f.stateDir, "state-dir", "", "state directory (default: $XDG_STATE_HOME/claude-inbox or ~/.local/state/claude-inbox)")
	flags.StringVar(&f.roots, "roots", strings.Join(cfg.Roots, ","), "comma-separated folders whose git repositories can host sessions, besides those Claude ran in before")
	flags.StringVar(&f.workspaces, "workspaces", strings.Join(cfg.Workspaces, ","), "comma-separated further folders a session can start in, for work across repositories")
	flags.StringVar(&f.claude, "claude", "claude", "claude command")
	flags.StringVar(&f.ide, "ide", cfg.IDE, "command that opens a folder in the IDE; empty hides the button")
	flags.StringVar(&f.ticketPattern, "ticket-pattern", cfg.TicketPattern, "regexp that finds the ticket id in a git branch name; empty finds none")
	flags.StringVar(&f.stripPrefix, "strip-prefix", cfg.StripPrefix, "prefix left out of repository names in tmux session names")
	flags.StringVar(&f.embedOrigin, "embed-origin", cfg.EmbedOrigin, "the one other origin that may show the app in a frame and set its theme, e.g. http://127.0.0.1:7700")
	flags.BoolVar(&f.open, "open", false, "open the app in the default browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tickets, err := ticketPattern(f.ticketPattern)
	if err != nil {
		return err
	}
	embed, err := embedOrigin(f.embedOrigin)
	if err != nil {
		return err
	}
	for _, tool := range []string{"tmux", f.claude, "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is not installed or not on PATH", tool)
		}
	}

	root, err := stateRoot(f.stateDir)
	if err != nil {
		return err
	}
	self, err := executable()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tmuxClient := tmux.New("")
	sessions, err := session.New(ctx, session.Config{
		Store:               store.New(root),
		Tmux:                tmuxClient,
		TmuxAt:              func(socketPath string) session.Tmux { return tmux.NewAt(socketPath) },
		Alive:               proc.New(procRoot).Alive,
		Roots:               splitList(f.roots),
		Workspaces:          splitList(f.workspaces),
		RepoGroups:          cfg.RepoGroups,
		TicketPattern:       tickets,
		StripPrefix:         f.stripPrefix,
		HookCommand:         []string{self, "hook", "--state-dir", root},
		SendCommand:         []string{self, "send", "--state-dir", root},
		Claude:              f.claude,
		Transcripts:         filepath.Join(home, ".claude", "projects"),
		AddWorktree:         gitdiff.AddWorktree,
		CurrentBranch:       gitdiff.CurrentBranch,
		Now:                 time.Now,
		Logger:              logger,
		TrustWait:           trustWait,
		DeliveryTimeout:     deliveryTimeout,
		KeyPause:            keyPause,
		PollInterval:        pollInterval,
		DiscoverInterval:    discoverInterval,
		PaneCheckInterval:   paneCheckInterval,
		ScreenCheckInterval: screenCheckInterval,
		StallAfter:          stallAfter,
	})
	if err != nil {
		return err
	}
	token, err := server.NewToken()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: server.New(server.Config{
			Port: f.port, Token: token, Sessions: sessions, Diff: gitdiff.Compute,
			Commit: gitdiff.Commit, Push: gitdiff.Push, OpenPR: openPR(),
			OpenIDE: openIDE(f.ide, logger), Web: web.Files, StripPrefix: f.stripPrefix, EmbedOrigin: embed,
			PrimaryActions: cfg.PrimaryActions, SecondaryActions: cfg.SecondaryActions, Logger: logger,
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	go sessions.Run(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/?token=%s", f.port, token)
	if _, err := fmt.Fprintf(os.Stdout, "Session Inbox: %s\n", url); err != nil {
		logger.Error("print the app link", "err", err)
	}
	urlPath := filepath.Join(root, urlFile)
	if err := os.WriteFile(urlPath, []byte(url), urlFileMode); err != nil {
		logger.Error("save the app link", "err", err)
	}
	defer removeURLFile(urlPath, url, logger)
	if f.open {
		if err := openWith(browserCommand(), logger)(ctx, url); err != nil {
			logger.Error("open the browser", "err", err)
		}
	}

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

// openWith starts command with one argument without waiting for it; a goroutine reaps it when it exits.
func openWith(command string, logger *slog.Logger) func(ctx context.Context, arg string) error {
	return func(_ context.Context, arg string) error {
		cmd := exec.Command(command, arg)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", command, err)
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				logger.Error("external command failed", "command", command, "err", err)
			}
		}()
		return nil
	}
}

// browserCommand opens a URL in the default browser.
func browserCommand() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// openIDE is nil without an IDE command, which tells the server and the page there is none.
func openIDE(command string, logger *slog.Logger) func(ctx context.Context, arg string) error {
	if command == "" {
		return nil
	}
	return openWith(command, logger)
}

// openPR is nil without gh on PATH, which hides the Diff tab's Open PR.
func openPR() func(ctx context.Context, dir string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil
	}
	return gitdiff.OpenPR
}

// runTmuxName prints the name claude-tmux gives a session in the current folder, from the same config as serve:
// <ticket>-<repo>, where repo is the git top-level folder (else the current one) and ticket comes from its branch.
func runTmuxName() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	cfg, err := loadConfig(configPath(os.Getenv, home), home)
	if err != nil {
		return err
	}
	tickets, err := ticketPattern(cfg.TicketPattern)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find current folder: %w", err)
	}
	ctx := context.Background()
	if top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		dir = strings.TrimSpace(string(top))
	}
	branch, err := gitdiff.CurrentBranch(ctx, dir)
	if err != nil {
		// Outside a repository there is no branch, so no ticket.
		branch = ""
	}
	_, err = fmt.Println(session.TmuxBase(session.TicketIn(tickets, branch), filepath.Base(dir), cfg.StripPrefix))
	return err
}

// removeURLFile removes the link file unless a newer serve has written its own link there already.
func removeURLFile(path, url string, logger *slog.Logger) {
	data, err := os.ReadFile(path)
	if err != nil || string(data) != url {
		return
	}
	if err := os.Remove(path); err != nil {
		logger.Error("remove the app link", "err", err)
	}
}

func stateRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	return store.DefaultRoot()
}

// executable is the absolute path of this binary, which the hook settings point at.
func executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find own executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve own executable: %w", err)
	}
	return resolved, nil
}
