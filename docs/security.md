# Security

**The app can type into sessions that run commands on your machine. Anyone who can use it has a shell.** Treat
the login link like an SSH key: never expose the port, never share the link.

## What protects it

| | |
|---|---|
| Loopback only | `serve` listens on 127.0.0.1, on `--port` (7788 by default). Nothing on your network can reach it. |
| Per-run token | `serve` makes a random 32-byte token at startup and prints it in the login link. Opening the link sets an `HttpOnly`, `SameSite=Strict` cookie; every `/api/` request needs it, compared in constant time. Restarting `serve` makes a new token and invalidates the old link. |
| Host check | Every request needs a `Host` of `127.0.0.1:<port>` or `localhost:<port>`, which blocks DNS rebinding. |
| Origin check | Every request other than `GET` and `HEAD`, and the WebSocket, needs a matching `Origin`. |
| Framing | Every response carries `Content-Security-Policy: frame-ancestors 'self'`, plus `embed_origin` when you set one, so no other page can frame the app. |

A request with no token gets a 401 and the page tells you to open the link `serve` printed. The link is also in
`~/.local/state/claude-inbox/serve.url`, mode `0600`.

## run actions

A `run` action ([actions.md](actions.md)) executes the command you configured, with your own permissions, in the
session's folder. The page picks an action by its place in the list and can never send a command of its own — but
every `run` in your config is one click away for anyone who can open the app. Keep destructive commands out of it,
or put `confirm: true` on them.

## Commit, Push and Open PR

The Diff tab's buttons run `git` and `gh` in the session's folder with your own credentials: whoever can open the
app can commit, push a feature branch and open a draft pull request. They never force a push, never push the default
branch, and send no command of the page's own.

## The hook

The hook writes to the state directory and nothing else. It prints nothing, always exits 0 and takes a few
milliseconds, so it cannot break or slow a session — also while the app is not running.

## Embedding

`embed_origin` lets exactly one other local origin frame the app and set its theme. That page still needs the
login link, and messages from any other origin are ignored. See
[configuration.md](configuration.md#embedding).
