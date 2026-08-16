GO ?= go
BIN_DIR ?= bin

.PHONY: build test install

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal ./cmd/turnal
	$(GO) build -o $(BIN_DIR)/turnal-adapter-opencode ./cmd/turnal-adapter-opencode
	$(GO) build -o $(BIN_DIR)/turnal-adapter-copilot-cli ./cmd/turnal-adapter-copilot-cli
	$(GO) build -o $(BIN_DIR)/turnal-adapter-cursor ./cmd/turnal-adapter-cursor
	$(GO) build -o $(BIN_DIR)/turnal-adapter-pi ./cmd/turnal-adapter-pi

test:
	$(GO) test ./...

install:
	$(GO) install ./cmd/...
