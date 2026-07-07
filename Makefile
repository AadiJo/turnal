GO ?= go
BIN_DIR ?= bin

.PHONY: build test install

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/agent-vcs ./cmd/agent-vcs
	$(GO) build -o $(BIN_DIR)/acs ./cmd/acs

test:
	$(GO) test ./...

install:
	$(GO) install ./cmd/agent-vcs ./cmd/acs
