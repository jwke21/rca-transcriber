package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// pinger checks that a dependency is reachable. *pgxpool.Pool satisfies it.
type pinger interface {
	Ping(ctx context.Context) error
}

// newHealthHandler serves GET /healthz: 200 "ok" when db answers a ping
// within timeout, otherwise 503.
func newHealthHandler(db pinger, timeout time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "healthz: database ping failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
