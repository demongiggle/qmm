# Build and development helpers for the qmm CLI.

GO ?= go
GOFLAGS ?=
GO_TEST_FLAGS ?= -race -count=1 -timeout 120s

BINARY ?= qmm
INSTALL_NAME ?= qmm
PACKAGE ?= ./cmd/qmm
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
OUT_DIR ?= dist

# Release builds stamp the exact Git tag into the binary. Untagged builds report
# "dev", which makes `qmm update` install the latest release. Override VERSION
# to stamp a custom value.
VERSION ?= $(shell sh ./scripts/version.sh)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := all

.PHONY: all build clean test vet fmt fmt-check check install release help

all: build

build:
	@mkdir -p '$(dir $(BINARY))'
	$(GO) build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o '$(BINARY)' $(PACKAGE)

test:
	$(GO) test $(GO_TEST_FLAGS) ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || { \
		echo "Go files need formatting; run 'make fmt':"; \
		gofmt -l .; \
		exit 1; \
	}

check: fmt-check vet build test

install: build
	install -d '$(DESTDIR)$(BINDIR)'
	install -m 0755 '$(BINARY)' '$(DESTDIR)$(BINDIR)/$(INSTALL_NAME)'

# Produce the qmm_<os>_<arch> assets consumed by `qmm update`.
release:
	VERSION='$(VERSION)' OUT_DIR='$(OUT_DIR)' GO='$(GO)' ./scripts/build-all.sh

clean:
	rm -f '$(BINARY)'
	rm -rf '$(OUT_DIR)'

help:
	@printf 'Usage: make [target]\n\n'
	@printf 'Targets:\n'
	@printf '  build       Build %s (default)\n' '$(BINARY)'
	@printf '  install     Install %s into %s\n' '$(INSTALL_NAME)' '$(BINDIR)'
	@printf '  release     Build release assets into %s\n' '$(OUT_DIR)'
	@printf '  test        Run tests with the race detector\n'
	@printf '  vet         Run go vet\n'
	@printf '  fmt         Format Go sources\n'
	@printf '  fmt-check   Verify Go source formatting\n'
	@printf '  check       Format check, vet, build, and test\n'
	@printf '  clean       Remove %s\n' '$(BINARY)'
	@printf '  help        Show this help\n'
	@printf '\nCommon overrides: BINARY, INSTALL_NAME, GOFLAGS, GO_TEST_FLAGS,\n'
	@printf '                 PREFIX, BINDIR, DESTDIR, VERSION, LDFLAGS,\n'
	@printf '                 OUT_DIR\n'
	@printf '\nCurrent version: %s\n' '$(VERSION)'
