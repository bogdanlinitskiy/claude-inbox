# build, test and lint run in Docker; integration tests and the app itself run on the host (docs/architecture.md, Testing).
# Images are pinned by digest, and -mod=readonly plus GOTOOLCHAIN=local keep any command from changing a
# dependency version or downloading a toolchain.
GO_IMAGE   := golang:1.26@sha256:dc2521c2a906db43073b8b4d99f491b6341cf15610b6ebbab187c45153f9959e
LINT_IMAGE := golangci/golangci-lint:v2.9.0@sha256:dbe337da33f7d649126107cd2cf0375e932c156c1e20e32a2d5205a79ba8c7a8
CACHE      := $(CURDIR)/.cache
# The version is stamped on the host, so the image needs no git of its own.
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "(devel)")

DOCKER := docker run --rm -u $(shell id -u):$(shell id -g) \
	-v $(CURDIR):/src -w /src -v $(CACHE):/cache \
	-e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOLANGCI_LINT_CACHE=/cache/lint \
	-e GOFLAGS=-mod=readonly -e GOTOOLCHAIN=local

INSTALL_DIR := $(HOME)/.local/bin
# build cross-compiles for this machine, so make install also works on a Mac (the image itself is Linux).
GOOS   := $(shell uname -s | tr '[:upper:]' '[:lower:]')
GOARCH := $(subst x86_64,amd64,$(subst aarch64,arm64,$(shell uname -m)))

.PHONY: build test lint test-integration test-e2e install run stop

build: | $(CACHE)
	$(DOCKER) -e CGO_ENABLED=0 -e GOOS=$(GOOS) -e GOARCH=$(GOARCH) $(GO_IMAGE) go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/claude-inbox ./cmd/claude-inbox

test: | $(CACHE)
	$(DOCKER) -e CGO_ENABLED=1 $(GO_IMAGE) go test -race ./...

lint: | $(CACHE)
	$(DOCKER) $(LINT_IMAGE) golangci-lint run ./...

# On the host: these tests drive the real tmux (on a private socket) and, later, a real claude.
test-integration: | $(CACHE)
	GOMODCACHE=$(CACHE)/mod GOFLAGS=-mod=readonly GOTOOLCHAIN=local go test -tags integration -count=1 ./...

# On the host, against a real claude: costs tokens, takes minutes.
test-e2e: | $(CACHE)
	INBOX_E2E=1 GOMODCACHE=$(CACHE)/mod GOFLAGS=-mod=readonly GOTOOLCHAIN=local go test -tags integration -count=1 -timeout 30m -v -run 'TestEndToEnd|TestDiscoveredSession' ./internal/e2e/

# The hook settings point at the binary that runs serve, so it has to stay at one fixed path.
install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0755 bin/claude-inbox $(INSTALL_DIR)/claude-inbox
	install -m 0755 scripts/claude-tmux $(INSTALL_DIR)/claude-tmux

run: install stop
	$(INSTALL_DIR)/claude-inbox serve --open

# Stops every running claude-inbox serve, which frees the port; the Claude sessions keep running in tmux. The
# pattern is anchored to the start of the command line, so hook runs and this recipe's own shell never match.
# A serve paused with Ctrl+Z acts on the stop signal only once resumed, hence the CONT after it.
SERVE_PATTERN := ^([^ ]*/)?claude-inbox serve
stop:
	@pkill -f '$(SERVE_PATTERN)' && echo "Stopping claude-inbox serve" || true
	@pkill -CONT -f '$(SERVE_PATTERN)' || true
	@for i in $$(seq 20); do pgrep -f '$(SERVE_PATTERN)' >/dev/null || exit 0; sleep 0.25; done; \
		echo "claude-inbox serve is still running" >&2; exit 1

$(CACHE):
	mkdir -p $@
