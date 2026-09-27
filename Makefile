BINARY  := rca-transcriber
BIN_DIR := bin
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)

.PHONY: unittest format build run

unittest:
	go test -race -count=1 -cover ./...

format:
	go fmt ./...

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(BINARY) ./cmd/server

run: build
	./$(BIN_DIR)/$(BINARY)
