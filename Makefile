# Build and development helpers for the cogito CLI.

GO ?= go
GOFLAGS ?=
GO_TEST_FLAGS ?= -race -count=1 -timeout 120s

BINARY ?= cogito
INSTALL_NAME ?= cogito
PACKAGE ?= ./cmd/cogito
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=

.DEFAULT_GOAL := all

.PHONY: all build clean test vet fmt fmt-check check install help

all: build

build:
	@mkdir -p '$(dir $(BINARY))'
	$(GO) build $(GOFLAGS) -o '$(BINARY)' $(PACKAGE)

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

clean:
	rm -f '$(BINARY)'

help:
	@printf 'Usage: make [target]\n\n'
	@printf 'Targets:\n'
	@printf '  build       Build %s (default)\n' '$(BINARY)'
	@printf '  install     Install %s into %s\n' '$(INSTALL_NAME)' '$(BINDIR)'
	@printf '  test        Run tests with the race detector\n'
	@printf '  vet         Run go vet\n'
	@printf '  fmt         Format Go sources\n'
	@printf '  fmt-check   Verify Go source formatting\n'
	@printf '  check       Format check, vet, build, and test\n'
	@printf '  clean       Remove %s\n' '$(BINARY)'
	@printf '  help        Show this help\n'
	@printf '\nCommon overrides: BINARY, INSTALL_NAME, GOFLAGS, GO_TEST_FLAGS,\n'
	@printf '                 PREFIX, BINDIR, DESTDIR\n'
