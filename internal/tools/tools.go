//go:build tools

// Package tools pins third-party module dependencies via blank imports so
// `go mod tidy` keeps them in go.mod/go.sum while Phase 1 work packages are
// being implemented in parallel and may not yet import every module
// themselves. WP9 deletes this package once all adapters are wired.
package tools

import (
	_ "github.com/google/go-github/v66/github"
	_ "github.com/gorilla/mux"
	_ "github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/joho/godotenv"
	_ "github.com/pashagolub/pgxmock/v4"
	_ "github.com/stretchr/testify/require"
	_ "github.com/twilio/twilio-go/client"
	_ "google.golang.org/genai"
)
