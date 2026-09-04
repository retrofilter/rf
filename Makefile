.PHONY: shell test lint build install install-service integration handbook templ webshot

# Bare `make` builds; the shell only starts when asked (make shell)
.DEFAULT_GOAL := build

# Start the AI shell (command mode, chat mode, Scheme evaluation)
shell:
	go run main.go

test:
	go test ./... $(ARGS)

# Comment rules (tools/commentlint): docs only on exported declarations, three
# lines max, no floating blocks, test files brief inline comments only.
# `make lint ARGS=-fix` deletes the offenders.
lint:
	go vet ./...
	go run ./tools/commentlint $(ARGS) ./...

# -s -w strips DWARF and the symbol table (~10MB); panics still have stacks.
# VERSION stamps `rf --version`: the nearest tag (v0.1.0, or v0.1.0-3-gabcdef
# past it), "devel" when there is none — the commit itself comes from Go's
# own vcs stamping, so it is never repeated here. Releases go through
# goreleaser (.goreleaser.yaml), which stamps the same variable from the
# tag. Override: make build VERSION=v0.2.0
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo devel)
LDFLAGS = -s -w -X github.com/retrofilter/rf/cmd.version=$(VERSION)
build:
	go build -ldflags="$(LDFLAGS)" -o rf .

# Default to the Homebrew prefix when brew is present (/opt/homebrew on Apple
# Silicon, /usr/local on Intel — both user-writable), else /usr/local (Linux
# usually needs: make build && sudo make install). Override: make install BINDIR=~/bin
PREFIX ?= $(shell brew --prefix 2>/dev/null || echo /usr/local)
BINDIR ?= $(PREFIX)/bin

# Copies only, never compiles — so `make build && sudo make install` doesn't
# rebuild as root (whose empty Go caches would re-download every module).
install:
	@test -f rf || { echo "no rf binary — run 'make build' first"; exit 1; }
	install -d $(BINDIR)
	install rf $(BINDIR)/rf

# Install and start the console systemd unit (Linux): sudo make install-service
# Runs the console as the invoking user ($SUDO_USER); override with SERVICE_USER=name.
# The unit's ExecStart expects rf at /usr/local/bin/rf (see contrib/systemd/).
SERVICE_USER ?= $(or $(SUDO_USER),$(USER))

install-service:
	install -m 644 contrib/systemd/rf-console@.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now rf-console@$(SERVICE_USER)

integration:
	go test -v ./integration/ $(ARGS)

# Regenerate HANDBOOK.md from the command registry (TestHandbookCurrent
# fails when a registration changes without this)
handbook:
	go run . handbook > HANDBOOK.md

# Regenerate console/*_templ.go from console/*.templ (generated files are checked
# in, so builds don't need the CLI: go install github.com/a-h/templ/cmd/templ@latest)
templ:
	templ generate -path console

# Screenshot the console's flows with headless Chrome (a dev tool — mainly
# for Claude to *see* UI changes — never part of go test). PNGs land in
# tmp/webshots/.
webshot:
	go run ./console/webshot $(ARGS)
