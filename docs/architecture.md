# Architecture

How Session Inbox works inside. What it does for the user is in the [README](../README.md). Code comments cite the
sections of this file.

## Overview

One Go binary, `claude-inbox`, with three commands at its core:

- `claude-inbox serve` runs the local web app: it starts Claude sessions in tmux, follows their hook events,
  serves the page, and types answers into the sessions.
- `claude-inbox hook` is the hook command. It appends the hook payload to the session's event file and exits.
- `claude-inbox send` is run by a session of a group to message another one, through `serve`
  ([Session groups](#session-groups)).

The hook writes to files, so it works while the app is down. The app turns a session's events into its current
state with one pure function, tested against a recorded run (`testdata/spike-hooks.jsonl`).

```
 browser page ──HTTP/SSE/WS──▶ claude-inbox serve ──tmux new-session / send-keys──▶ tmux ── claude
      ▲                               │  ▲                                                   │
      │ SSE: session changed          │  │ polls every 200 ms                                │ hook events
      └───────────────────────────────┘  └──────── ~/.local/state/claude-inbox/events/<id>.jsonl ◀── claude-inbox hook
```

## State directory

`$XDG_STATE_HOME/claude-inbox/`, else `~/.local/state/claude-inbox/` (created by `serve`, mode 0700):

| Path | Written by | Content |
|---|---|---|
| `sessions/<id>.json` | serve, hook | Metadata: id, ticket, title, repo, dir, tmux name, created at; for discovered sessions also pid, tmux socket and pane. |
| `events/<id>.jsonl` | hook and serve | One JSON object per line: `{ts, source: "hook", payload}` or `{ts, source: "app", event}`. |
| `hook-settings.json` | serve, at start-up | The settings passed with `--settings`; points every hook at the absolute path of the binary. |
| `archive/` | serve | Metadata and events of ended sessions, for debugging; `serve` deletes files older than 30 days when it starts. |
| `groups/<group id>.jsonl` | serve | The messages the sessions of a group sent each other: `{sentAt, from, to, text}` per line. |
| `serve.url` | serve | The login link of the running app; `claude-inbox send` reads it to reach the app. |

App events (`source: "app"`): `interrupt` (Esc sent), `denied` (No picked on a permission menu), `trust_prompt`
(the trust screen is showing), `pane_gone` (tmux session vanished), `process_gone` (the `claude` process of a
discovered session is gone), `closed`, `screen_prompt` (a menu no hook reported is on the screen; the payload is
that menu as a question), `screen_prompt_gone` (that menu left the screen unanswered) and `answered` (an answer
from the app took a menu off the screen before any hook said so) and `stalled` (a working session of a group has
fired nothing for 5 minutes). The last four carry a `seq`, so an event that
arrives after the session has moved on changes nothing. Interrupt and Deny fire no hook, the trust screen comes
before any hook, and Claude Code asks some things without a hook at all, so the app records them itself. Keeping
them in the same file keeps one ordered history per session, so a restart replays exactly what happened.

## Hook modes

Two hook modes:

- **App sessions**: `claude-inbox hook --state-dir <dir>`, passed with `claude --settings hook-settings.json`. The
  app picks a random 8-character id, passes it as `INBOX_SESSION` (`tmux new-session -e`), and the hook records into
  that id's file. Without `INBOX_SESSION` it does nothing.
- **Discovered sessions**: `claude-inbox hook --discover`, in the global `~/.claude/settings.json`, so it runs for
  every Claude session on the machine. With `INBOX_SESSION` set it does nothing (the app-mode hook of the same
  session already records it). Otherwise the id is the payload's `session_id`, which must be a lowercase UUID
  because it becomes a file name; anything else is dropped with a line on stderr. If `sessions/<id>.json` does not
  exist yet, the hook writes it first (temporary file + `link`, so two hooks racing on the first event leave one
  whole file), then appends the payload.

`claude-inbox setup` adds the global hook. It edits `settings.json` in place: keys keep their order, values it
does not touch keep their exact bytes, and the file keeps its indent. A hook counts as the app's when its command
runs a binary named `claude-inbox` with `hook --discover`, however the path is written or quoted, so a second run
adds nothing. It shows the diff, asks, backs the file up (never overwriting a backup), follows a symlinked file,
writes through a temporary file and a rename, and refuses when the file changed while it was asking. `--remove`
takes out every such hook, then entries and events left empty.

Both register the same ten events (`hook.Events`), matcher `*`, timeout 5 s: `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`, `PermissionDenied`, `Stop`, `StopFailure`.
`--settings` hooks run alongside the global ones, so an app session runs both.

The hook never gets in a session's way: it finishes in a few milliseconds (1.7 ms on average, 14 ms with a 1 MiB
payload), prints nothing to stdout, and always exits 0, even when it cannot write.

The binary lives at a fixed path (`~/.local/bin/claude-inbox`, `make install`), because running sessions keep the
hook path they were started with.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/claude-inbox` | The config file, flag parsing, and the `serve`, `hook`, `setup`, `send` and `tmux-name` commands. |
| `internal/settings` | Adds the global hook to Claude Code's `settings.json` and takes it out, editing the JSON in place. |
| `internal/hook` | Read stdin, wrap it in `{ts, source, payload}`, append it to the event file under `flock`. |
| `internal/store` | State directory layout, session metadata, appending app events, reading event files from a saved offset, archiving. |
| `internal/state` | `Apply(Session, Event) Session`, the pure state machine, plus the view model sent to the page. |
| `internal/session` | The manager: start, close, answers with delivery confirmation, event polling, pane, process, branch and trust checks, replay. |
| `internal/tmux` | Thin wrapper over the `tmux` CLI. Targets are exact: `=name`, or a pane id `%<n>`. |
| `internal/answer` | Turns an answer into a key sequence; reads menus and their numbers from a captured screen. Everything that depends on how Claude Code words and draws that screen is in `wording.go`, so a release that changes the output has one file to re-read. |
| `internal/proc` | Finds the `claude` process of a discovered session and tells whether it still runs: `/proc` on Linux, `ps` on macOS. |
| `internal/transcript` | Reads Claude Code's transcripts (`~/.claude/projects/<folder>/<id>.jsonl`) for History and Preview. |
| `internal/gitdiff` | Default-branch detection, merge base, `--numstat`, patch, untracked files, worktrees. |
| `internal/action` | The configured actions: validation, the `{input}` placeholder, and running a `run` action's command (own process group, timeout, output tail). |
| `internal/server` | Routes, auth middleware, the SSE hub, session handlers, the terminal WebSocket. |
| `web/` | `index.html`, its stylesheets in `css/` (one per cascade layer), its scripts in `js/` (one per feature, loaded in order) and vendored xterm.js, embedded with `go:embed`. |

Third-party modules: `github.com/creack/pty` (a PTY for `tmux attach`), `github.com/coder/websocket` (terminal
socket), `github.com/stretchr/testify` (tests). Everything else is the standard library.

## State machine

Session state: `kind` (Trust, Idle, Working, Question, Permission, Done, Ended, Unknown), `since`, `seq`
(incremented on every change), plus the content of the current kind. Trust counts as "needs you".

| Event | New state |
|---|---|
| any hook event with a non-empty `agent_id` | unchanged |
| `SessionStart` | Idle |
| `UserPromptSubmit`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied` | Working |
| `PreToolUse` | Working; activity = tool name plus its `description` or `command` |
| `PermissionRequest`, `tool_name` = `AskUserQuestion` | Question; questions with options and `multiSelect` |
| `PermissionRequest`, any other tool | Permission; tool, command, description |
| `PermissionRequest` whose `tool_input` does not parse | Unknown |
| `Stop` | Done; text = `last_assistant_message` |
| `StopFailure` | Done, marked as an error |
| `SessionEnd` with `reason` `clear` or `resume` | unchanged: the next conversation starts in the same process |
| `SessionEnd`, app `pane_gone`, `process_gone`, `closed` | Ended |
| app `trust_prompt` | Trust |
| app `interrupt`, app `denied` | Idle |
| app `screen_prompt` | Question read from the screen, marked `fromScreen`; ignored when a hook already said what the session waits on |
| app `screen_prompt_gone` for the current `seq` | back to the state the session was in before the menu appeared |
| app `stalled` for the current `seq`, while Working | Working, marked `stalled`, which counts as "needs you"; the next event of any kind clears it |
| app `answered` for the current `seq` | Working after a question or a permission, Idle after the trust screen, else back as for `screen_prompt_gone` |
| any other event | unchanged |

## Answers to keys

| Answer | Keys |
|---|---|
| Question, option *i* (0-based) | `i+1` |
| Question, free text | `len(options)+1`, then the text as literal, then `Enter` |
| Question with several sub-questions or `multiSelect` | refused; the page says "Answer in the terminal" |
| Permission, Allow | `1` |
| Permission, Deny | the number of the `No` entry, read from `capture-pane`, then an app `denied` event |
| Permission, free text | Deny, then the text as a normal message (after Deny, Claude waits for "what to do instead") |
| Message (Working, Done, Idle) | the text as literal, then `Enter`; while Working, Claude queues it |
| Interrupt | `Escape`, then an app `interrupt` event |
| Trust the folder / don't | `Down`, `Enter` / `Enter` (Claude exits) |

Option numbers shift with the number of options and the permission mode, so keys are computed from the payload or
read from the screen, never hardcoded. Text with line breaks is refused: a newline would submit early.

Every answer request carries the state's `seq`. The server refuses it with 409 if the session has moved on, so an
answer never lands in a different prompt than the one on screen.

A sent message counts as delivered only when its `UserPromptSubmit` arrives. If none arrives within 3 s, the
server sends `Enter` once more; if there is still none after another 3 s, the page shows "Not sent. Check the
terminal."

## HTTP API

All under `127.0.0.1:<port>` (default 7788). One session is `/api/session/{id}`, the list is `/api/sessions`.
Errors are `{message, code}`.

| Route | Purpose |
|---|---|
| `GET /?token=…` | Sets the auth cookie (HttpOnly, SameSite=Strict) and redirects to `/`. |
| `GET /` | The page. |
| `GET /api/sessions` | All sessions with their state. |
| `POST /api/sessions` | Start a session: `{dir, ticket, title, prompt, worktree, branch}`. |
| `POST /api/groups` | Start a group: `{dirs, ticket, title, prompt, worktree, branch, coordinator, shareDirs}`; answers the started sessions. |
| `GET /api/group/{id}/messages` | The messages the group's sessions sent each other, oldest first. |
| `GET /api/session/{id}` | One session. |
| `PATCH /api/session/{id}` | Rename: `{title}`; an empty title shows the folder name again. |
| `DELETE /api/session/{id}` | App session: close (`C-c`, `/exit`, `Enter`, wait up to 5 s for `SessionEnd`, kill the tmux session, archive). Discovered session: hide (archive, no keys sent). |
| `POST /api/session/{id}/answer` | `{seq, option}` or `{seq, text}` or `{seq, allow}`. |
| `POST /api/session/{id}/message` | `{text}`. |
| `POST /api/session/{id}/send` | `{to, text}`: session `{id}` messages the session of its group named `to` (tmux name or id); see [Session groups](#session-groups). |
| `POST /api/session/{id}/interrupt` | Sends Esc. |
| `POST /api/session/{id}/action` | `{list, index, input}`: the action at `index` of `primary` or `secondary`. A `send` action goes through the message path (`{input}` replaced) and answers `{session}`. A `run` action runs `sh -c <command>` in the session's folder with `$INBOX_INPUT`, in its own process group, killed after 5 minutes, and answers `{run: {exitCode, output, truncated, timedOut}}` with the last 16 KiB of output; 409 while the session is working or its folder is unknown. An unknown action or a missing input is a 400. |
| `GET /api/session/{id}/diff` | Files with +/−, patch, untracked files. |
| `POST /api/session/{id}/open-ide` | Runs the `--ide` command on the session's folder (detached); 404 without one. |
| `GET /api/session/{id}/terminal` | WebSocket: the live terminal. |
| `GET /api/conversations` | History: past conversations from the transcripts that no listed session holds, latest first. |
| `GET /api/conversation/{id}/preview` | The first prompt, the last three prompts and Claude's last reply. |
| `POST /api/conversation/{id}/resume` | `claude --resume <id>` in its folder as a new app session. 409 while it runs in a listed session or when its folder is gone. |
| `GET /api/repos` | The folders a session can start in, in the start form's order, each with the `group` heading it is listed under: the repositories Claude ran in before (latest first), the workspaces, then the other repositories under the roots; or, with `repo_groups` configured, the groups' repositories and workspaces in config order, then the workspaces no group lists. |
| `GET /api/config` | `{ide, cheatSheet, stripPrefix, embedOrigin, primaryActions, secondaryActions}`: which optional buttons the page shows, the prefix it leaves out of repository names, the origin that may frame it, and the actions as `{label, kind, text, input, confirm}` (`kind` is `send` or `run`, `text` the prompt or command). |
| `GET /api/stream` | SSE: one message per state change, flagged `live`. |
| `GET /cheatsheet` | Redirects to the `--cheatsheet` URL. |

Auth: `serve` makes a random 32-byte token per run and prints the login URL. Every `/api/` request needs the cookie
(constant-time compare). Every request needs a `Host` of `127.0.0.1:<port>` or `localhost:<port>` (blocks DNS
rebinding). Every request other than `GET` and `HEAD`, and the WebSocket, also needs a matching `Origin`. Every
response carries `Content-Security-Policy: frame-ancestors 'self'`, plus `embed_origin` when set, so no other page
can frame the app (clickjacking).

## Starting a session

1. The folder must be one that `GET /api/repos` lists, so a request cannot start Claude in an arbitrary folder.
   The ticket is free text; if empty, it is the first match of the ticket pattern in `git branch --show-current`.
2. With `worktree`, the app first checks `branch` out into a new git worktree next to the repository.
3. Pick the tmux name `<ticket>-<repo>` (`no-ticket-<repo>` without one), with the strip prefix left out of the
   repository name; add `-2`, `-3`… if it exists.
4. Write the metadata, then run
   `tmux new-session -d -s <name> -c <dir> -e INBOX_SESSION=<id> -x 200 -y 50 -- claude --settings <hook-settings.json> --permission-mode auto <prompt>`.
5. Set `status off` and `mouse on` on that tmux session: no tmux bar in the page, and the mouse wheel scrolls.
6. If no `SessionStart` arrives within 3 s, capture the pane; if it shows the trust question, append
   `trust_prompt`. The page then asks "Trust this folder?".

## Session groups

`POST /api/groups` starts one session per repository in `dirs`, on one ticket, as a group with a new 8-character
group id, which every member keeps in its metadata (`group`) next to its part (`role`: `coordinator` or
`worker`).

1. Each folder must be listed, as for a single session. With `worktree`, every worktree is made first, so a bad
   branch starts nothing.
2. With `coordinator`, one more session runs in the deepest folder that holds all the repositories, named
   `<ticket>-coordinator`.
3. Every tmux name is reserved before any session starts, so each session's system prompt can name all of them.
4. Each session runs `claude … [--add-dir <the other members' folders>] --append-system-prompt <the group> [<prompt>]`.
   The system prompt lists the members (name, role, repository, folder), says how to message one, and gives the
   session its part. `--add-dir` takes several values, so the system prompt sits between it and the prompt. With
   a coordinator only the coordinator gets the prompt; without one every session does.
5. A session that fails to start leaves the others running; the request answers with the error.

`claude-inbox send <name> <text>` reads the sender's id from `INBOX_SESSION` and the app's link and token from
`serve.url`, and posts to `POST /api/session/{id}/send` with the token cookie and the app's `Origin`. The manager
finds the target by tmux name or id among the sender's group, sends `[from <sender>] <text>` as a normal message
(queued while the target works), and appends the message to `groups/<group id>.jsonl` for the Group tab.

The app also tells a coordinator what its workers cannot or did not, as messages from `claude-inbox`. Watching the
live events of each worker (never replayed ones), it makes a notice when a worker:

- ends a turn without having run `send` since the turn's `UserPromptSubmit`: one started by the user typing to it
  directly (the notice quotes the prompt and the last reply), or one started by a group message it did not answer;
- stops on `StopFailure`;
- ends (`SessionEnd`, `pane_gone`, `closed`);
- stalls: no event of any kind for 5 minutes while Working. A lost connection fires no hook, so silence is the only
  sign of it. The app records `stalled`, which also puts the session in front of the user.

Each poll sends a coordinator its waiting notices as one message, unless it has a send on its way or waits on the
user; then they wait for a later poll. Two messages in a row to an idle session would start with two `C-c`s, which
Claude Code reads as exit. Notices for a group without a running coordinator are dropped.

## Following events

One goroutine owned by `serve` polls the event files every 200 ms from their saved offsets, runs new lines through
`state.Apply`, and publishes every change to the SSE hub. Every second it also lists `sessions/`, so a discovered
session appears within a second of its first event; that listing is separate from the 200 ms poll because reading
the events of sessions already on the list is what has to be prompt. Every 5 s the app checks that each app
session's tmux pane still exists (`pane_gone` otherwise) and that each discovered session's `claude` process still
runs (`process_gone`). A session that reaches Ended is archived and leaves the list; its conversation is then in
History.

Hooks do not cover everything Claude Code asks: a new MCP server, a usage limit, and other prompts reach the
screen with no hook behind them. So the app reads the screen (`capture-pane`) of each session that runs in tmux,
waits on nothing a hook reported, has no answer on its way, and has been quiet for 2 s. A numbered menu there
becomes a `screen_prompt`, which the page shows as a question tagged "from screen"; when the menu leaves the
screen unanswered, a `screen_prompt_gone` puts the session back where it was. A session whose screen keeps
showing the same menu is not recorded again.

The wait between those reads starts at 2 s and doubles, up to 30 s, for every read that finds nothing — a session
left finished overnight costs a handful of reads instead of one every two seconds. Anything worth watching puts it
straight back to 2 s: a menu on the screen, a question the session is already showing (whose leaving has to be
noticed quickly), or any event at all. The cost is that a menu appearing on a long-quiet session is seen up to
30 s late.

On start-up `serve` replays every file and publishes nothing until the replay is done, so no stale alerts fire
(only `live` changes alert).

## Discovered sessions

The hook walks up from its parent (at most 5 levels) to the first process whose `/proc/<pid>/comm` is `claude`,
and records its pid and start time (field 22 of `/proc/<pid>/stat`). A pid with a different start time counts as
gone, so a reused pid is not mistaken for the session. Not found → pid 0, and the session leaves only on
`SessionEnd` or Hide.

macOS has no `/proc`: there the hook runs `ps -A -o pid=,ppid=,lstart=,args=` once (in the C locale) and walks the
same way, taking a process as `claude` when its first argument is `claude` or a path ending in it; the start time
is `lstart` in Unix seconds. The liveness check asks `ps -p <pid>`. The parsing is plain code (`ps.go`), tested on
every system; only the `ps` call is macOS-only.

`TMUX` gives the socket path (its first comma-separated field) and `TMUX_PANE` the pane. A discovered session in
tmux is answered, messaged, interrupted and shown in the Terminal tab exactly like an app session, through its
pane on its socket (`tmux -S`). Outside tmux it is view-only: those requests return 409 "Answer it where it runs."

`scripts/claude-tmux` starts claude inside a new tmux session, so a session started in an IDE terminal can be
answered too. `claude-inbox tmux-name` gives it the name, from the same config as `serve`: `<ticket>-<repo>`, or
just `<repo>` without a ticket. It passes `TERMINAL_EMULATOR`, `CLAUDE_CODE_SSE_PORT` and
`ENABLE_IDE_INTEGRATION` on, so `/ide` keeps working, and prints the resume command when claude exits.

## Terminal

The WebSocket handler starts `tmux attach-session -t <target>` in a PTY (`TERM=xterm-256color`), copies bytes both
ways, and resizes the PTY on `{"type":"resize","cols":…,"rows":…}` messages. The PTY must be the attach process's
controlling terminal (`pty.Start` does this); otherwise tmux never sees the resize. The page sends its size in the
socket URL too, because a resize sent while the tmux client starts can be lost. Closing the socket kills the attach
process, which only detaches; the session keeps running.

## Diff

Base: `git symbolic-ref --short refs/remotes/origin/HEAD`, else the first of `origin/master`, `master`,
`origin/main`, `main` that exists. Then `git merge-base HEAD <base>`, `git diff --numstat <mb>` and `git diff <mb>`
(working tree, so uncommitted changes count), plus `git ls-files --others --exclude-standard` for new files.
The patch is capped at 1 MiB and untracked files at 500. A workspace has no single repository, so its Diff tab
says so.

## Page

Plain HTML, CSS and JavaScript, no build step. xterm.js and the fonts are vendored in `web/vendor/` (versions and
checksums in `VERSIONS`) and embedded, so the page loads nothing from the internet. The Terminal tab is an xterm.js instance on the WebSocket. Alerts use
the browser Notification API: on a `live` change into Question or Permission, the page shows a notification (tag =
session id), and clicking it focuses the window and selects that session. The Alerts toggle is kept in
`localStorage`. A `#session=<id>` link selects that session.

Actions: the primary chips render inside the composer, along the bottom of the reply box and beside its Send and
Interrupt buttons (at most 60 drawn); `fitBar` keeps the ones of the first two rows, measured from the bar's own
top, behind a "+N more" chip. A state with nothing to type into shows the same row on its own (`.composer.bare`). The menu (`#act-menu`, fixed, outside the
detail so redraws leave it alone) draws its search box once and redraws only its rows, at most 200, so typing keeps
the caret. An action greys out with the reason when the session cannot take it (a `send` while a question waits, a
`run` while Claude works); the server checks again. A `confirm` action arms on the first click, like Close.

Framed by `embed_origin`, the page adds the `framed` class (transparent background), posts `home:theme?` to the
parent and applies the `home:theme` answers, which it accepts from that origin only. Its own requests still go to
its own origin, so the embed origin is not added to the allowed `Origin`s.

## Edge cases

| Case | Handling |
|---|---|
| Events from internal agents | Ignored when `agent_id` is set. |
| Interrupt and Deny fire no hook | The app appends `interrupt` or `denied` after sending the keys. |
| Folder not trusted yet | Trust screen detected after start → Trust state; the page offers Trust / Don't trust. |
| An Enter does not submit | Delivery is confirmed by `UserPromptSubmit`; one retry, then "Not sent. Check the terminal." |
| Answered in the terminal instead of the app | `PostToolUse` moves the state on; a later app answer gets 409 because `seq` changed. |
| tmux pane disappears without `SessionEnd` | The 5 s check appends `pane_gone` → Ended. |
| Claude Code changes a payload | An unparsable `PermissionRequest` gives Unknown; the page shows "Unknown state" and the Terminal tab. Unknown event names are ignored. |
| App down while states changed | Replay at start-up; only changes seen afterwards are `live` and alert. |
| Hook cannot write (disk full, permissions) | Exits 0 anyway, so Claude is not affected; the session's state goes stale, and the Terminal tab still works. |
| `tmux`, `claude` or `git` missing | `serve` exits at start with a message naming the missing one. |
| Token missing or wrong | 401, and the page says to open the link `serve` printed. |
| A session already running when the global hook was installed | Appears on its next hook event. |

## Testing

- **Unit** (`make test`, `make lint`, in Docker): none of them needs tmux or `claude`; the diff tests use the
  image's `git`.
- **Integration** (`make test-integration`, on the host, `//go:build integration`): the tmux wrapper and the
  terminal socket against a real tmux on a private socket.
- **End-to-end** (`make test-e2e`, on the host, opt-in): a real `claude` through the HTTP API: question, free text,
  permission, done, interrupt, close, restart, and a discovered session. Costs tokens and takes minutes.
- **Fixtures**: a recorded hook run for events, captured screens for menu numbers. When a Claude Code update
  changes either, re-capture them and the tests show what moved.

Build, unit tests and lint run in Docker with pinned images; integration tests and the app itself run on the host,
because they drive the host's tmux, `claude` and repositories.
