// Command server runs the RCA Voice Transcriber: it answers Twilio voice
// webhooks, streams call audio to Deepgram, stores the transcript in
// PostgreSQL and turns it into an RCA pull request via Gemini and GitHub.
//
// This is the composition root, the only place adapters are constructed and
// wired into the core services.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jwke21/rca-transcriber/internal/adapter/deepgram"
	"github.com/jwke21/rca-transcriber/internal/adapter/gemini"
	"github.com/jwke21/rca-transcriber/internal/adapter/github"
	"github.com/jwke21/rca-transcriber/internal/adapter/postgres"
	"github.com/jwke21/rca-transcriber/internal/adapter/twilio"
	"github.com/jwke21/rca-transcriber/internal/config"
	"github.com/jwke21/rca-transcriber/internal/core/service"
)

const (
	readHeaderTimeout   = 10 * time.Second
	serverShutdownLimit = 10 * time.Second
	rcaShutdownLimit    = 60 * time.Second
	healthPingTimeout   = 2 * time.Second
	twilioVoicePath     = "/twilio/voice"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run loads the configuration, opens the database pool and serves until
// SIGINT or SIGTERM. The pool is closed last, after serve has shut down the
// HTTP server and the RCA service.
func run() error {
	// 1. Configuration.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Logger.
	logger := newLogger(cfg.LogLevel)

	// Cancelled on SIGINT/SIGTERM, so a signal during startup also aborts
	// the database connection attempts.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 3. Database pool.
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	serveErr := serve(ctx, cfg, logger, pool)

	// Shutdown step 3: the pool, after everything that uses it has stopped.
	logger.Info("shutdown: closing database pool")
	pool.Close()
	logger.Info("shutdown complete")
	return serveErr
}

// serve wires the adapters and services, recovers pending RCAs, runs the HTTP
// server until ctx is cancelled (or the server fails), then shuts down the
// HTTP server and the RCA service in that order.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) error {
	// 4. Repositories.
	engineers := postgres.NewEngineerRepository(pool)
	incidents := postgres.NewIncidentRepository(pool)
	events := postgres.NewIncidentEventRepository(pool)

	// 5. Outbound adapters.
	stt := deepgram.New(deepgram.Config{
		APIKey: cfg.DeepgramAPIKey,
		Model:  cfg.DeepgramModel,
	}, logger)

	generator, err := gemini.New(ctx, gemini.Config{
		APIKey: cfg.GeminiAPIKey,
		Model:  cfg.GeminiModel,
	}, logger)
	if err != nil {
		return fmt.Errorf("create gemini generator: %w", err)
	}

	publisher, err := github.New(github.Config{
		Token:      cfg.GitHubToken,
		Owner:      cfg.GitHubOwner,
		Repo:       cfg.GitHubRepo,
		BaseBranch: cfg.GitHubBaseBranch,
	}, logger)
	if err != nil {
		return fmt.Errorf("create github publisher: %w", err)
	}

	// 6. Clock.
	clock := service.SystemClock{}

	// 7–9. Core services. The RCA service is the call service's RCATrigger.
	rcaService := service.NewRCAService(incidents, events, generator, publisher, clock, logger)
	callService := service.NewCallService(engineers, incidents, rcaService, clock, logger)
	transcriptionService := service.NewTranscriptionService(stt, events, logger)

	// 10. Startup recovery (NFR4). Failures are logged, not fatal.
	if err := rcaService.RecoverPending(ctx); err != nil {
		logger.Error("startup: recover pending RCAs", "error", err)
	}

	// 11. Router.
	router := mux.NewRouter()
	twilio.NewHandler(twilio.Config{
		AccountSID:          cfg.TwilioAccountSID,
		AuthToken:           cfg.TwilioAuthToken,
		PublicBaseURL:       cfg.PublicBaseURL,
		StreamSigningSecret: cfg.StreamSigningSecret,
	}, callService, transcriptionService, logger).Register(router)
	router.Handle("/healthz", newHealthHandler(pool, healthPingTimeout, logger)).Methods(http.MethodGet)

	// 12. HTTP server.
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// 13. Effective configuration and the webhook URL for the Twilio console.
	logger.Info("starting rca-transcriber", "config", cfg.Redacted())
	logger.Info("set the Twilio number's 'A call comes in' webhook to POST this URL",
		"webhook_url", cfg.PublicBaseURL+twilioVoicePath)

	serverErrs := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrs <- err
			return
		}
		serverErrs <- nil
	}()

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErrs:
		if err != nil {
			runErr = fmt.Errorf("http server: %w", err)
			logger.Error("http server failed", "error", err)
		}
	}

	shutdown(server, rcaService, logger)
	return runErr
}

// shutdown stops the HTTP server (no new calls; open media WebSockets drain as
// their handlers return), then waits for in-flight RCA generations. Each step
// is bounded and its error logged, and the next step runs regardless.
func shutdown(server *http.Server, rcaService *service.RCAService, logger *slog.Logger) {
	logger.Info("shutdown: stopping http server", "timeout", serverShutdownLimit)
	serverCtx, cancelServer := context.WithTimeout(context.Background(), serverShutdownLimit)
	if err := server.Shutdown(serverCtx); err != nil {
		logger.Error("shutdown: http server", "error", err)
	}
	cancelServer()

	logger.Info("shutdown: waiting for in-flight RCA generations", "timeout", rcaShutdownLimit)
	rcaCtx, cancelRCA := context.WithTimeout(context.Background(), rcaShutdownLimit)
	if err := rcaService.Shutdown(rcaCtx); err != nil {
		logger.Error("shutdown: rca service", "error", err)
	}
	cancelRCA()
}

// newLogger returns a text slog.Logger on stdout at the given level. config
// has already validated level; anything unparsable falls back to info.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
