BINARY      := rca-transcriber
BIN_DIR     := bin
GOOS        ?= $(shell go env GOOS)
GOARCH      ?= $(shell go env GOARCH)
PORT        ?= 8080
NGROK_DOMAIN ?=
DATABASE_URL ?= postgres://rca:rca@localhost:5432/rca?sslmode=disable

.PHONY: unittest format build run db ngrok add-engineer

unittest:
	go test -race -count=1 -cover ./...

format:
	go fmt ./...

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(BINARY) ./cmd/server

run: build
	./$(BIN_DIR)/$(BINARY)

# Starts Postgres and applies Flyway migrations.
db:
	docker compose up -d

# Tunnels localhost:$(PORT) so Twilio webhooks can reach the local server.
# Set PUBLIC_BASE_URL in .env to the https URL ngrok prints. A random
# ngrok-free.app URL changes every restart, requiring PUBLIC_BASE_URL and the
# Twilio Console webhook to be updated each time; pass a reserved domain
# (NGROK_DOMAIN=your-name.ngrok-free.app) to avoid that.
ngrok:
	ngrok http $(if $(NGROK_DOMAIN),--domain=$(NGROK_DOMAIN) )$(PORT)

# Registers an engineer's phone number so their calls are accepted.
# Usage: make add-engineer PHONE=+15555550123
# PHONE must be E.164. Re-adding an existing number is a no-op.
add-engineer:
	@echo '$(PHONE)' | grep -Eq '^\+[1-9][0-9]{1,14}$$' || { echo "PHONE must be E.164, e.g. make add-engineer PHONE=+15555550123" >&2; exit 1; }
	@echo "INSERT INTO engineers (phone_number) VALUES (:'phone') ON CONFLICT (phone_number) DO NOTHING;" | \
		psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -v phone='$(PHONE)'
