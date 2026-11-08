# Configuration

Nothing is required. To change the defaults, create `~/.config/claude-inbox/config.json` (or
`$XDG_CONFIG_HOME/claude-inbox/config.json`). Every key is optional, `~` expands, and an unknown key is refused,
so a typo shows up at once instead of being ignored.

```json
{
  "roots": ["~/work"],
  "workspaces": ["~/work/shared"],
  "ide": "code",
  "cheatsheet": "",
  "ticket_pattern": "[A-Z][A-Z0-9]+-\d+",
  "strip_prefix": "acme-",
  "embed_origin": ""
}
```

## Keys

| Key / flag | Meaning |
|---|---|
| `roots` / `--roots` | Folders whose git repositories can host sessions, besides the ones Claude ran in before (read from `~/.claude/projects`). Each root is also offered as a workspace. Comma-separated as a flag. |
| `workspaces` / `--workspaces` | Further folders a session can start in, for work across repositories. |
| `repo_groups` (file only) | Your own repository list for the start form, in place of the recent and other repositories. See [repositories.md](repositories.md). |
| `primary_actions`, `secondary_actions` (file only) | Your own buttons for the selected session. See [actions.md](actions.md). |
| `ide` / `--ide` | Command that opens a folder in your IDE, e.g. `code`. Empty (default) hides the button. |
| `cheatsheet` / `--cheatsheet` | URL the Cheat sheet button in the header opens. Empty (default) hides the button. |
| `ticket_pattern` / `--ticket-pattern` | Go regexp that finds the ticket id in a git branch name; it names the session and groups its rows in the list. Default `[A-Z][A-Z0-9]+-\d+` (Jira style); empty finds none. |
| `strip_prefix` / `--strip-prefix` | Prefix left out of repository names in the list and in tmux session names, e.g. `acme-` turns `acme-wallet` into `wallet`. |
| `embed_origin` / `--embed-origin` | The one other origin, `scheme://host[:port]`, that may show the app in a frame, e.g. a local dashboard. Empty (default): no other page can frame it. See [Embedding](#embedding). |

A flag overrides the file. These exist as flags only:

| Flag | Default |
|---|---|
| `--port` | `7788` (always on 127.0.0.1) |
| `--claude` | `claude` — the command to start |
| `--state-dir` | `$XDG_STATE_HOME/claude-inbox`, else `~/.local/state/claude-inbox` |
| `--open` | off — open the app in the default browser |

`claude-inbox serve -h` prints them with the config file path it reads.

## Embedding

With `embed_origin` set, that one page can show the app in an `<iframe>`. Framed there, the app makes its
background transparent and follows the page's theme: it posts `{type: "home:theme?"}` to the parent, and the
parent answers `{type: "home:theme", theme: "light" | "dark"}`, again on every switch.

Messages from any other origin are ignored, and the browser refuses to frame the app anywhere else
(`Content-Security-Policy: frame-ancestors`). The framed page still needs the login link, and its requests still
come from the app's own origin.
