# Session Inbox

A local web app for running many Claude Code sessions at once. It lists every session on your machine, grouped by
ticket, puts the ones waiting on a question or a permission at the top, and lets you answer them, watch their live
terminal and read their diff, all in one window. A desktop alert fires when a session starts waiting.

Claude Code keeps running in a real terminal: each session runs inside tmux, and the app types your answers into
its pane. You never have to use tmux yourself.

## Features

- **Inbox**: sessions grouped by ticket, waiting ones first, each with its state (Working, Question, Permission,
  Done) and how long it has been in it. A waiting session shows its question, or the command it wants to run,
  right in the list.
- **Answer from the app**: option buttons and free text for questions, Allow / Deny for permissions, a message box
  and Interrupt for a working session, and Trust / Don't trust for a new folder.
- **Terminal tab**: the session's live tmux pane, readable, scrollable and typeable.
- **Diff tab**: `git diff` against the merge base with the repository's default branch, uncommitted and untracked
  files included.
- **New session**: pick a repository or workspace, a ticket, a title and a first message; optionally in a new git
  worktree. Claude starts in auto mode. The repositories Claude ran in before come first, with no setup; or list
  your own groups with `repo_groups`.
- **Discovered sessions**: with the global hook installed, sessions you start yourself are listed too. Those
  running in tmux (see `claude-tmux`) can be answered; the others are view-only.
- **Actions**: your own buttons, as chips over the reply box (`primary_actions`) and in a searchable Actions menu
  (`secondary_actions`, or press `.`). One types a prompt or a slash command into the session, the other runs a
  shell command in its folder and shows the output.
- **History**: past conversations from Claude Code's transcripts, searchable, with a preview and Resume.
- **Alerts**: a desktop notification when a session starts waiting; clicking it selects that session.
- **Restarts lose nothing**: sessions keep running in tmux, and the app rebuilds their state from the recorded
  events.

## Requirements

- Linux or macOS. On macOS the app builds and its macOS-only code is unit-tested, but it has not been run on a Mac
  yet; reports welcome.
- `tmux`, `git` and `claude` (Claude Code) on `PATH`.
- A browser for the page; `serve --open` uses `xdg-open` (`open` on macOS).
- To build: Go 1.26, or Docker for `make`.

## Install

With Go:

```bash
go install github.com/bogdanlinitskiy/claude-inbox/app/cmd/claude-inbox@latest
```

This puts `claude-inbox` in `$(go env GOPATH)/bin`. `claude-tmux` is a script that comes with the repository
(`app/scripts/claude-tmux`); copy it next to the binary if you want it.

From a clone, with Docker and nothing else:

```bash
cd app
make install    # builds in Docker, installs claude-inbox and claude-tmux to ~/.local/bin
```

`claude-inbox version` prints what you run. Keep the binary where you installed it: running sessions call their
hook at the path they were started with.

### Global hook (optional)

Sessions started from the app need nothing more. To also list the sessions you start yourself, run:

```bash
claude-inbox setup
```

It adds a hook to `~/.claude/settings.json` for the events `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`, `PermissionDenied`, `Stop` and
`StopFailure`, next to the hooks you already have. It shows the exact diff and asks before writing (`--yes` skips
the question), keeps a backup next to the file (`settings.json.bak-<time>`), and leaves the rest of the file as it
was: key order, values and indent. Running it again changes nothing; `claude-inbox setup --remove` takes the hook
out. Running sessions pick the change up on their next event.

The hook takes a few milliseconds, prints nothing and always exits 0, so it never gets in a session's way, also
while the app is not running.

## Usage

```bash
claude-inbox serve
```

It prints a login link (also saved to `~/.local/state/claude-inbox/serve.url`); open it in the browser. `--open`
opens it in the default browser.

### Configuration

Nothing is required. To change the defaults, create `~/.config/claude-inbox/config.json` (or
`$XDG_CONFIG_HOME/claude-inbox/config.json`); every key is optional, `~` expands, and unknown keys are refused:

```json
{
  "roots": ["~/work"],
  "workspaces": ["~/work/shared"],
  "ide": "code",
  "cheatsheet": "",
  "ticket_pattern": "[A-Z][A-Z0-9]+-\\d+",
  "strip_prefix": "acme-",
  "embed_origin": ""
}
```

| Key / flag | Meaning |
|---|---|
| `roots` / `--roots` | Folders whose git repositories can host sessions, besides the ones Claude ran in before (read from `~/.claude/projects`). Each root is also offered as a workspace. Comma-separated as a flag. |
| `workspaces` / `--workspaces` | Further folders a session can start in, for work across repositories. |
| `repo_groups` (file only) | Your own repository list for the start form, in place of the recent and other repositories; see below. |
| `primary_actions`, `secondary_actions` (file only) | Buttons for the selected session: chips over the reply box, and the searchable Actions menu; see below. |
| `ide` / `--ide` | Command that opens a folder in your IDE, e.g. `code`. Empty (default) hides the button. |
| `cheatsheet` / `--cheatsheet` | URL the Cheat sheet button opens. Empty (default) hides the button. |
| `ticket_pattern` / `--ticket-pattern` | Go regexp that finds the ticket id in a git branch name; sessions are grouped by it. Default `[A-Z][A-Z0-9]+-\d+` (Jira style); empty finds none. |
| `strip_prefix` / `--strip-prefix` | Prefix left out of repository names in the list and in tmux session names, e.g. `acme-` turns `acme-wallet` into `wallet`. |
| `embed_origin` / `--embed-origin` | The one other origin, `scheme://host[:port]`, that may show the app in a frame, e.g. a local dashboard. Empty (default): no other page can frame it. See [Embedding](#embedding). |

`repo_groups` lists the start form's repositories top to bottom, under headings:

```json
"repo_groups": [
  {"title": "Important", "repos": [{"some": "~/work/{wallet, gateway}"}, {"repo": "~/libs/constants"}]},
  {"title": "", "repos": [{"repo": "~/tools/scripts"}]},
  {"title": "Other", "repos": [{"all": "~/work"}, {"all": "~/libs"}]}
]
```

`repo` is one repository, `some` the named repositories of a folder, `all` every git repository directly in a
folder, `workspace` a folder for work across repositories. An empty title lists its repositories under no heading.
A repository is listed once, at its first place, so `all` after `some` lists the rest. Workspaces (`roots`,
`workspaces`) no group lists follow the groups. A named repository
that is not a git repository is an error in the start form, so a typo shows.

`primary_actions` become chips over the reply box, two rows at most; the rest sit behind a "+N more" chip that
opens them in a searchable list. `secondary_actions` fill the Actions menu in the session's header (or press `.`),
which opens with its search box focused. Both lists take the same entries, an action may be in both, and neither
has a size limit:

```json
"primary_actions": [
  {"label": "Commit", "send": "Commit the changes made during this session"},
  {"label": "Ultracave", "send": "/ultracave"},
  {"label": "Ticket…", "send": "Work on {input}", "input": "Ticket id"}
],
"secondary_actions": [
  {"label": "Pull", "run": "git pull --ff-only", "confirm": true},
  {"label": "Switch branch", "run": "git switch \"$INBOX_INPUT\"", "input": "Branch"}
]
```

Each entry has a `label` and exactly one of:

- `send`: text typed into the session as a prompt, like the reply box does, so a slash command works too. Available
  while the session is idle, finished or working (a working session reads it after the current step), not while it
  waits on a question.
- `run`: a `sh` command run in the session's folder, not seen by Claude. The output and exit code show in a
  notice. Refused while the session is working, so it cannot race Claude's edits; stopped after 5 minutes.

`input` asks for one line before acting: a `send` puts it where `{input}` is, a `run` gets it in `$INBOX_INPUT`,
never pasted into the command. `confirm` asks for a second click. A malformed entry stops `serve` with its name.

Flags override the file. Only for flags: `--port` (default 7788), `--claude` (the `claude` command) and
`--state-dir` (default `$XDG_STATE_HOME/claude-inbox` or `~/.local/state/claude-inbox`).

To make a session you start in an IDE terminal answerable, run `claude-tmux` instead of `claude`. It starts claude
in a new tmux session in that terminal, named like the app names its own (`claude-inbox tmux-name`), passes the
arguments to claude, keeps `/ide` working, and prints the resume command when claude exits.

### Embedding

With `embed_origin` set, that page can show the app in an `<iframe>`. Framed there, the app makes its background
transparent and follows the page's theme: it posts `{type: "home:theme?"}` to the parent, and the parent answers
`{type: "home:theme", theme: "light" | "dark"}`, again on every switch. Messages from any other origin are
ignored, and the browser refuses to frame the app anywhere else (`Content-Security-Policy: frame-ancestors`). The
framed page still needs the login link, and its requests still come from the app's own origin.

## Security

The app can type into sessions that run commands on your machine, so anyone who can use it has a shell. It listens
on 127.0.0.1 only, and `run` actions execute whatever commands your config lists; the page can only pick one of
them by its place in the list, never send a command of its own. Every request needs the per-run token from the login link, requests with another `Host` or
`Origin` are refused, and only the app itself (plus `embed_origin`, if set) may frame it. Never expose the port, and
do not share the login link.

## Development

All commands run in `app/`:

```bash
make build             # in Docker
make test              # in Docker
make lint              # in Docker
make test-integration  # on the host, drives a real tmux on a private socket
make test-e2e          # on the host, against a real claude: costs tokens, takes minutes
```

How it works inside: [docs/architecture.md](docs/architecture.md).

## License

MIT, see [LICENSE](LICENSE). The vendored xterm.js is MIT too (`app/web/vendor/LICENSE-xterm`).
