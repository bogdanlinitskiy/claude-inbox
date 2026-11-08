# Development

One Go module at the repository root: `cmd/claude-inbox` is the binary, `internal/` its packages, `web/` the page
it embeds, `scripts/` the `claude-tmux` wrapper.

Build, unit tests and lint run in Docker with images pinned by digest, so they need nothing installed but Docker.
The integration and end-to-end tests run on the host, because they drive the host's tmux, `claude` and
repositories.

```bash
make build             # in Docker, to bin/claude-inbox
make test              # unit tests with -race, in Docker
make lint              # golangci-lint, in Docker
make test-integration  # on the host, drives a real tmux on a private socket
make test-e2e          # on the host, against a real claude: costs tokens, takes minutes
make install           # build and install to ~/.local/bin
make run               # install, stop a running serve, start it with --open
make stop              # stop every running serve; the Claude sessions keep running
```

`GOFLAGS=-mod=readonly` and `GOTOOLCHAIN=local` keep any command from changing a dependency version or
downloading a toolchain. The version is stamped on the host from `git describe`, so the build image needs no git.

## Tests

- **Unit** — `make test`, everything that needs no tmux.
- **Integration** — `make test-integration`, build tag `integration`: the tmux wrapper and the session machinery
  against a real tmux on a private socket.
- **End-to-end** — `make test-e2e`, opt-in: a real `claude` through the HTTP API — question, free text,
  permission, done, interrupt, close, restart, and a discovered session.

## Where to look

[architecture.md](architecture.md) is the design: the state directory, the two hook modes, the packages, the state
machine, how answers become key presses, and the HTTP API. Code comments cite its sections, so keep it in step
when you change behaviour.
