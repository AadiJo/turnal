GO ?= go
BIN_DIR ?= bin

.PHONY: build build-collector build-telemetry-admin analytics-fixtures test install

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal ./cmd/turnal

build-collector:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal-collector ./cmd/turnal-collector

build-telemetry-admin:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/turnal-telemetry-admin ./cmd/turnal-telemetry-admin

analytics-fixtures:
	$(GO) run ./cmd/turnal-analytics-fixtures

test:
	$(GO) test ./...

install:
	$(GO) install ./cmd/turnal
