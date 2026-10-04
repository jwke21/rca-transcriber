BINARY      := rca-transcriber
BIN_DIR     := bin
GOOS        ?= $(shell go env GOOS)
GOARCH      ?= $(shell go env GOARCH)
PORT        ?= 8080
NGROK_DOMAIN ?=

.PHONY: unittest format build run ngrok

unittest:
	go test -race -count=1 -cover ./...

format:
	go fmt ./...

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(BINARY) ./cmd/server

run: build
	./$(BIN_DIR)/$(BINARY)

# Tunnels localhost:$(PORT) so Twilio webhooks can reach the local server.
# Set PUBLIC_BASE_URL in .env to the https URL ngrok prints. A random
# ngrok-free.app URL changes every restart, requiring PUBLIC_BASE_URL and the
# Twilio Console webhook to be updated each time; pass a reserved domain
# (NGROK_DOMAIN=your-name.ngrok-free.app) to avoid that.
ngrok:
	ngrok http $(if $(NGROK_DOMAIN),--domain=$(NGROK_DOMAIN) )$(PORT)
