# Makefile — Nexus build system
# Injects version from git tag into binaries (ADR-030 for local builds).
# Usage:
#   make           — build all binaries to ~/bin/
#   make engx      — build engx only
#   make engxd     — build engxd only (CGO required for sqlite3)
#   make engxa     — build engxa only
#   make build     — alias for all
#   make clean     — remove build artifacts from ~/bin/

VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDENGX   := -X main.cliVersion=$(VERSION)
LDENGXD  := -X main.daemonVersion=$(VERSION)
BINDIR   := $(HOME)/bin

.PHONY: all build engx engxd engxa clean

all: build

build: engx engxd engxa

engx:
	@echo "  → engx $(VERSION)"
	@CGO_ENABLED=0 go build -ldflags "$(LDENGX)" -o $(BINDIR)/engx ./cmd/engx/

engxd:
	@echo "  → engxd $(VERSION)"
	@CGO_ENABLED=1 go build -ldflags "$(LDENGXD)" -o $(BINDIR)/engxd ./cmd/engxd/

engxa:
	@echo "  → engxa $(VERSION)"
	@CGO_ENABLED=0 go build -o $(BINDIR)/engxa ./cmd/engxa/

clean:
	@rm -f $(BINDIR)/engx $(BINDIR)/engxd $(BINDIR)/engxa
	@echo "  cleaned $(BINDIR)/engx engxd engxa"
