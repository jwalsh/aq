.PHONY: all build test clean lint fmt install help build-all test-race version-info quickstart check-up-to-date

GO ?= go
BINARY := aq
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -ldflags "-s -w \
	-X main.Version=$(VERSION) \
	-X main.GitCommit=$(COMMIT) \
	-X main.BuildDate=$(BUILD_DATE)"

GO_FILES := $(shell find . -name '*.go' -type f 2>/dev/null)

PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 freebsd-amd64

all: build

build: $(BINARY)

$(BINARY): $(GO_FILES)
	$(GO) build $(LDFLAGS) -o $@ .

define build-platform
$(BINARY)-$(1): $(GO_FILES)
	GOOS=$$(echo $(1) | cut -d- -f1) GOARCH=$$(echo $(1) | cut -d- -f2) \
		$(GO) build $(LDFLAGS) -o $$@ .
endef

$(foreach p,$(PLATFORMS),$(eval $(call build-platform,$(p))))

build-all: $(addprefix $(BINARY)-,$(PLATFORMS))

check-up-to-date:
ifndef SKIP_UPDATE_CHECK
	@git fetch origin main --quiet 2>/dev/null || true
	@LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse origin/main 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with origin/main"; \
		echo "Run 'git pull' first, or use SKIP_UPDATE_CHECK=1 to override"; \
		exit 1; \
	fi
endif

install: check-up-to-date build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY); do \
		if [ -f "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

lint:
	@echo "==> Go: vet"
	$(GO) vet ./...
	@echo "==> Go: fmt check"
	@gofmt -l $(GO_FILES) | tee /dev/stderr | (! read)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "==> Go: golangci-lint"; \
		golangci-lint run; \
	fi

fmt:
	gofmt -w $(GO_FILES)

version-info:
	@echo "VERSION:    $(VERSION)"
	@echo "COMMIT:     $(COMMIT)"
	@echo "BUILD_DATE: $(BUILD_DATE)"

quickstart: build
	./$(BINARY) quickstart

clean:
	rm -f $(BINARY) $(BINARY)-*

help:
	@echo "aq Makefile targets:"
	@echo ""
	@echo "Build:"
	@echo "  make build       Build the aq binary"
	@echo "  make build-all   Cross-compile for all platforms"
	@echo "  make install     Build and install to ~/.local/bin"
	@echo ""
	@echo "Test:"
	@echo "  make test        Run tests"
	@echo "  make test-race   Run tests with race detector"
	@echo ""
	@echo "Lint:"
	@echo "  make lint        Run go vet, fmt check, golangci-lint"
	@echo "  make fmt         Auto-format Go files"
	@echo ""
	@echo "Other:"
	@echo "  make clean        Remove build artifacts"
	@echo "  make version-info Show embedded version info"
	@echo "  make quickstart   Build and run quickstart"
	@echo "  make help         Show this help"
	@echo ""
	@echo "Variables:"
	@echo "  GO=go1.23        Use alternate go binary"
	@echo "  SKIP_UPDATE_CHECK=1  Skip origin/main freshness check"
	@echo ""
	@echo "FreeBSD: use gmake instead of make"
