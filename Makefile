GO ?= go
BIN_DIR ?= bin

.PHONY: build build-go test install

build:
	npm run build:web
	$(MAKE) build-go

build-go:
	$(GO) build -o "$(BIN_DIR)/" ./cmd/...

test:
	$(GO) test ./...

install:
	$(GO) install ./cmd/...
