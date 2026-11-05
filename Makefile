GO ?= go
PREFIX ?= $(HOME)/.local
SOAK_DURATION ?= 48h
SOAK_TIMEOUT ?= 49h

.PHONY: build install fmt test check integration soak

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/onji ./cmd/onji
	$(GO) build -trimpath -o bin/onjid ./cmd/onjid

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/onji bin/onjid $(PREFIX)/bin/

fmt:
	gofmt -w cmd internal integration

test:
	$(GO) test -race ./...

check:
	test -z "$$(gofmt -l cmd internal integration)"
	$(GO) vet ./...
	$(GO) test -race ./...
	$(MAKE) build

integration:
	ONJI_INTEGRATION=1 $(GO) test -race -count=1 -v ./integration

soak:
	ONJI_SOAK_DURATION=$(SOAK_DURATION) $(GO) test -race -count=1 -v -run '^TestSoak$$' -timeout $(SOAK_TIMEOUT) ./internal/daemon
