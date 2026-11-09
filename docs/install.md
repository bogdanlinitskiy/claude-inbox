# Install

## Requirements

- Linux or macOS. On macOS the app builds and its macOS-only code is unit-tested, but it has not been run on a Mac
  yet; reports welcome. On Windows it runs under WSL, see [On Windows, with WSL](#on-windows-with-wsl); it does not
  run on Windows itself, because it drives `tmux` and reads `/proc`.
- `tmux`, `git` and `claude` (Claude Code) on `PATH`. `serve` refuses to start without them.
- A browser for the page. `serve --open` uses `xdg-open` (`open` on macOS).
- To build: Go 1.26, or Docker for `make`.

## With Go

```bash
go install github.com/bogdanlinitskiy/claude-inbox/cmd/claude-inbox@latest
```

That puts `claude-inbox` in `$(go env GOPATH)/bin`. `claude-tmux` is a script that comes with the repository
(`scripts/claude-tmux`); copy it next to the binary if you want it.

## From a clone, with Docker and nothing else

```bash
make install
```

It builds in Docker and installs `claude-inbox` and `claude-tmux` to `~/.local/bin`.

Keep the binary where you installed it: a running session calls its hook at the path it was started with, so a
moved binary leaves those sessions unable to report. `claude-inbox version` prints what you run.

## On Windows, with WSL

The app runs inside a WSL2 distribution, and you use it from your Windows browser.

1. Install WSL2 and a distribution, if you have none:

   ```powershell
   wsl --install -d Ubuntu
   ```

2. In the WSL shell, install the requirements and Claude Code:

   ```bash
   sudo apt update && sudo apt install -y tmux git
   ```

3. Install `claude-inbox` the same way as on Linux, with Go or with `make install` from a clone.

4. Start it:

   ```bash
   claude-inbox serve
   ```

Open the printed link in your Windows browser. WSL2 forwards `127.0.0.1` from the distribution to Windows, so
`http://127.0.0.1:7788` reaches the app with no extra setup. Leave the shell running; closing it stops `serve`,
though the Claude sessions keep running in tmux.

Two things differ from a plain Linux install:

- `serve --open` calls `xdg-open`, which a bare WSL distribution does not have. Either open the printed link by
  hand, or install [`wslu`](https://github.com/wslutilities/wslu) (`sudo apt install wslu`) and link `wslview` as
  `xdg-open`.
- Keep the repositories you work in inside the WSL filesystem (`~/code`, not `/mnt/c/...`). Git and Claude Code
  are much slower across `/mnt/c`, and file watching there is unreliable.

To open a session's folder in a Windows editor, point `--ide` at its WSL launcher, for example `code` for
VS Code with the WSL extension, which already resolves WSL paths.

## Where things live

| Path | What |
|---|---|
| `~/.local/state/claude-inbox/` | Recorded events, one file per session, and `serve.url` — the login link of the running `serve`. Override with `--state-dir`. |
| `~/.config/claude-inbox/config.json` | Optional configuration, see [configuration.md](configuration.md). `$XDG_CONFIG_HOME` is honoured. |
| `~/.claude/settings.json` | Where `claude-inbox setup` adds the global hook. |

## Global hook

Sessions started from the app need nothing more. To also list the sessions you start yourself:

```bash
claude-inbox setup
```

It adds one hook to `~/.claude/settings.json`, next to the hooks you already have, for the events `SessionStart`,
`SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`,
`PermissionDenied`, `Stop` and `StopFailure`.

It shows the exact diff and asks before writing (`--yes` skips the question), keeps a backup next to the file
(`settings.json.bak-<time>`), and leaves the rest of the file as it was: key order, values and indent. Running it
again changes nothing. Running sessions pick the change up on their next event.

The hook takes a few milliseconds, prints nothing and always exits 0, so it never gets in a session's way — also
while the app is not running.

## claude-tmux

A discovered session can be read, but it can only be answered if it runs inside tmux. To make a session you start
in an IDE terminal answerable, run `claude-tmux` instead of `claude`:

```bash
claude-tmux            # takes the same arguments as claude
```

It starts claude in a new tmux session in that terminal, named the way the app names its own
(`claude-inbox tmux-name`), keeps `/ide` working, and prints the resume command when claude exits.

## Upgrading

Install again the same way. Sessions keep running in tmux across a restart of `serve`, and their state is rebuilt
from the recorded events.

## Uninstalling

```bash
claude-inbox setup --remove
```

Then delete the binary, `~/.local/state/claude-inbox` and `~/.config/claude-inbox`. Claude sessions running in
tmux are untouched; attach to them with `tmux attach`.
