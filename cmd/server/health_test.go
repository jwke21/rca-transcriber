package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePinger calls ping with the handler's context.
type fakePinger struct {
	ping func(ctx context.Context) error
}

func (f fakePinger) Ping(ctx context.Context) error { return f.ping(ctx) }

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		ping       func(ctx context.Context) error
		wantStatus int
		wantBody   string
	}{
		{
			name:    "ping succeeds",
			timeout: time.Minute,
			ping: func(ctx context.Context) error {
				// The handler must bound the ping with a deadline.
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("ping context has no deadline")
				}
				return nil
			},
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "ping fails",
			timeout:    time.Minute,
			ping:       func(context.Context) error { return errors.New("connection refused") },
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unavailable",
		},
		{
			name:    "ping times out",
			timeout: time.Millisecond,
			ping: func(ctx context.Context) error {
				// Block until the handler's timeout fires, like an unresponsive database.
				<-ctx.Done()
				return ctx.Err()
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newHealthHandler(fakePinger{ping: tt.ping}, tt.timeout, slog.New(slog.DiscardHandler))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantBody, rec.Body.String())
			assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
		})
	}
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{level: "debug", want: slog.LevelDebug},
		{level: "info", want: slog.LevelInfo},
		{level: "warn", want: slog.LevelWarn},
		{level: "error", want: slog.LevelError},
		{level: "bogus", want: slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			logger := newLogger(tt.level)
			assert.True(t, logger.Enabled(context.Background(), tt.want))
			if tt.want > slog.LevelDebug {
				assert.False(t, logger.Enabled(context.Background(), tt.want-1))
			}
		})
	}
}
