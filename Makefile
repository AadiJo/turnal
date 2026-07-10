GO ?= go
BIN_DIR ?= bin

.PHONY: build build-collector test install

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal ./cmd/turnal

build-collector:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal-collector ./cmd/turnal-collector

test:
	$(GO) test ./...

install:
	$(GO) install ./cmd/turnal
