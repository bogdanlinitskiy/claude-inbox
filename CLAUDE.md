# claude-inbox (Session Inbox)

A local web app that lists every running Claude Code session, with the ones waiting on a question or permission on
top. It answers them by typing into their tmux pane, shows their live terminal and diff, and alerts when one starts
waiting.

One Go module at the repository root: `cmd/claude-inbox` is the binary, `internal/` its packages, `web/` the
page it embeds.

## How it works

One binary, `claude-inbox`, installed at `~/.local/bin/claude-inbox`:

- `serve` — the web app (http://127.0.0.1:7788/); starts sessions in tmux and saves its login link to
  `~/.local/state/claude-inbox/serve.url`.
- `hook --state-dir <dir>` — the `--settings` hook of sessions the app starts (keyed by `INBOX_SESSION`).
- `hook --discover` — the global hook in `~/.claude/settings.json`, for sessions started elsewhere; `setup` adds it
  there (`setup --remove` takes it out).

Hooks append events to `~/.local/state/claude-inbox/events/<id>.jsonl`; `serve` polls them and
`internal/state.Apply` turns them into each session's state. `scripts/claude-tmux` starts claude inside tmux from
an IDE terminal, so the app can answer it.

## Docs

- `README.md` — what it does, install, usage.
- `docs/architecture.md` — design: state directory, hook modes, packages, state machine, HTTP API. Code comments
  cite its sections.

## Commands

```bash
make build             # in Docker
make test              # in Docker
make lint              # in Docker
make test-integration  # on the host, drives a real tmux on a private socket
make install           # build and install to ~/.local/bin; running sessions and the global hook use this path
```

Never `make run` (opens a browser) or `make test-e2e` (a real claude, costs tokens, takes minutes) unless asked.
