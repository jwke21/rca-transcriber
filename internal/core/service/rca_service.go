package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

const (
	// rcaDefaultMaxAttempts is how many times generation and publishing are each tried.
	rcaDefaultMaxAttempts = 3
	// rcaDefaultGenerationTimeout bounds one background generation.
	rcaDefaultGenerationTimeout = 5 * time.Minute
	// rcaStatusTimeout bounds the terminal status write, which uses a context
	// detached from the generation context so it succeeds even after that expires.
	rcaStatusTimeout = 10 * time.Second
)

// rcaDefaultBackoff waits 1s, 2s, 4s, ... before retry number attempt (1-based).
func rcaDefaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 30 {
		attempt = 30
	}
	return time.Second << (attempt - 1)
}

// RCAOption configures an RCAService.
type RCAOption func(*RCAService)

// WithRCAMaxAttempts sets how many times generation and publishing are each
// attempted. Values below 1 are treated as 1.
func WithRCAMaxAttempts(n int) RCAOption {
	return func(s *RCAService) {
		if n < 1 {
			n = 1
		}
		s.maxAttempts = n
	}
}

// WithRCABackoff sets the wait before retry number attempt (1 for the wait
// after the first failure). A nil function keeps the default (1s, 2s, 4s).
func WithRCABackoff(backoff func(attempt int) time.Duration) RCAOption {
	return func(s *RCAService) {
		if backoff != nil {
			s.backoff = backoff
		}
	}
}

// WithRCAGenerationTimeout bounds each background generation started by
// Enqueue. Non-positive values keep the default (5 minutes).
func WithRCAGenerationTimeout(d time.Duration) RCAOption {
	return func(s *RCAService) {
		if d > 0 {
			s.generationTimeout = d
		}
	}
}

// RCAService runs the background RCA pipeline: claim the incident, load its
// transcript, generate a title and summary, render the Markdown RCA, publish
// it as a pull request and record the final status. Nothing is published when
// generation fails.
type RCAService struct {
	incidents port.IncidentRepository
	events    port.IncidentEventRepository
	generator port.RCAGenerator
	publisher port.RCAPublisher
	clock     port.Clock
	logger    *slog.Logger

	maxAttempts       int
	backoff           func(attempt int) time.Duration
	generationTimeout time.Duration

	// baseCtx parents every background generation. cancel aborts them when
	// Shutdown runs out of time.
	baseCtx context.Context
	cancel  context.CancelFunc

	// mu guards shuttingDown and idle, and serializes wg.Add in Enqueue with
	// the start of wg.Wait in Shutdown.
	mu           sync.Mutex
	shuttingDown bool
	idle         chan struct{} // closed once every in-flight generation has finished
	wg           sync.WaitGroup
}

var (
	_ port.RCAService = (*RCAService)(nil)
	_ port.RCATrigger = (*RCAService)(nil)
)

// NewRCAService returns an RCAService. A nil logger discards log output.
func NewRCAService(
	incidents port.IncidentRepository,
	events port.IncidentEventRepository,
	generator port.RCAGenerator,
	publisher port.RCAPublisher,
	clock port.Clock,
	logger *slog.Logger,
	opts ...RCAOption,
) *RCAService {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	s := &RCAService{
		incidents:         incidents,
		events:            events,
		generator:         generator,
		publisher:         publisher,
		clock:             clock,
		logger:            logger,
		maxAttempts:       rcaDefaultMaxAttempts,
		backoff:           rcaDefaultBackoff,
		generationTimeout: rcaDefaultGenerationTimeout,
		baseCtx:           baseCtx,
		cancel:            cancel,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Enqueue starts RCA generation for the incident in a background goroutine.
// It never blocks. After Shutdown has begun it logs a warning and does nothing.
func (s *RCAService) Enqueue(incidentID int64) {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		s.logger.Warn("rca: shutting down, not enqueueing generation", "incident_id", incidentID)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.baseCtx, s.generationTimeout)
		defer cancel()

		if err := s.Generate(ctx, incidentID); err != nil {
			s.logger.Error("rca: background generation failed", "incident_id", incidentID, "error", err)
			return
		}
		s.logger.Debug("rca: background generation finished", "incident_id", incidentID)
	}()
}

// Generate runs the full pipeline synchronously for one incident. It returns
// nil without doing anything if the incident isn't waiting for generation.
// Once the incident is claimed, every exit path records rca_complete or
// rca_failed.
func (s *RCAService) Generate(ctx context.Context, incidentID int64) error {
	claimed, err := s.incidents.ClaimForGeneration(ctx, incidentID)
	if err != nil {
		return fmt.Errorf("rca: claim incident %d: %w", incidentID, err)
	}
	if !claimed {
		s.logger.DebugContext(ctx, "rca: incident not pending, nothing to generate", "incident_id", incidentID)
		return nil
	}

	status := domain.IncidentStatusRCAFailed
	defer func() { s.setTerminalStatus(ctx, incidentID, status) }()

	incident, err := s.incidents.Get(ctx, incidentID)
	if err != nil {
		return fmt.Errorf("rca: get incident %d: %w", incidentID, err)
	}
	events, err := s.events.ListByIncident(ctx, incidentID)
	if err != nil {
		return fmt.Errorf("rca: list transcript for incident %d: %w", incidentID, err)
	}
	if len(events) == 0 {
		return fmt.Errorf("rca: incident %d: %w", incidentID, domain.ErrNoTranscript)
	}

	report, err := rcaRetry(ctx, s.maxAttempts, s.backoff, func(ctx context.Context) (domain.RCAReport, error) {
		report, err := s.generator.Generate(ctx, incident, events)
		if err != nil {
			return domain.RCAReport{}, err
		}
		if err := report.Validate(); err != nil {
			return domain.RCAReport{}, err
		}
		return report, nil
	})
	if err != nil {
		return fmt.Errorf("rca: generate report for incident %d: %w", incidentID, err)
	}

	var resolvedAt time.Time
	if incident.ResolutionTime != nil {
		resolvedAt = *incident.ResolutionTime
	} else {
		resolvedAt = s.clock.Now()
		s.logger.WarnContext(ctx, "rca: incident has no resolution time, using current time",
			"incident_id", incidentID, "resolved_at", resolvedAt.UTC())
	}

	doc := BuildRCADocument(incident, report, resolvedAt)
	prURL, err := rcaRetry(ctx, s.maxAttempts, s.backoff, func(ctx context.Context) (string, error) {
		return s.publisher.Publish(ctx, doc)
	})
	if err != nil {
		return fmt.Errorf("rca: publish report for incident %d: %w", incidentID, err)
	}

	status = domain.IncidentStatusRCAComplete
	s.logger.InfoContext(ctx, "rca: published", "incident_id", incidentID, "pr_url", prURL, "path", doc.Path)
	return nil
}

// setTerminalStatus records the final status on a context detached from ctx,
// so it is written even when ctx was cancelled or timed out.
func (s *RCAService) setTerminalStatus(ctx context.Context, incidentID int64, status domain.IncidentStatus) {
	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rcaStatusTimeout)
	defer cancel()
	if err := s.incidents.SetStatus(statusCtx, incidentID, status); err != nil {
		s.logger.ErrorContext(ctx, "rca: set final status failed",
			"incident_id", incidentID, "status", status, "error", err)
	}
}

// RecoverPending resets incidents left in rca_generating (orphans from a
// previous run, since there is a single instance) to rca_pending, then
// enqueues every rca_pending incident. All errors are joined.
func (s *RCAService) RecoverPending(ctx context.Context) error {
	var errs []error

	generating, err := s.incidents.ListByStatus(ctx, domain.IncidentStatusRCAGenerating)
	if err != nil {
		errs = append(errs, fmt.Errorf("rca: list generating incidents: %w", err))
	}
	for _, incident := range generating {
		if err := s.incidents.SetStatus(ctx, incident.ID, domain.IncidentStatusRCAPending); err != nil {
			errs = append(errs, fmt.Errorf("rca: reset incident %d to pending: %w", incident.ID, err))
			continue
		}
		s.logger.InfoContext(ctx, "rca: reset orphaned generation to pending", "incident_id", incident.ID)
	}

	pending, err := s.incidents.ListByStatus(ctx, domain.IncidentStatusRCAPending)
	if err != nil {
		errs = append(errs, fmt.Errorf("rca: list pending incidents: %w", err))
	}
	for _, incident := range pending {
		s.logger.InfoContext(ctx, "rca: recovering pending generation", "incident_id", incident.ID)
		s.Enqueue(incident.ID)
	}

	return errors.Join(errs...)
}

// Shutdown stops accepting new work and waits for in-flight generations. If
// ctx is done first, it cancels them and returns ctx.Err(). A cancelled
// generation records rca_failed, so it is not recovered at the next startup;
// the SRE retries by calling in and confirming again. It is safe to call more
// than once.
func (s *RCAService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.shuttingDown = true
	if s.idle == nil {
		// No Enqueue can call wg.Add from here on, so Wait never races Add.
		idle := make(chan struct{})
		s.idle = idle
		go func() {
			s.wg.Wait()
			close(idle)
		}()
	}
	idle := s.idle
	s.mu.Unlock()

	select {
	case <-idle:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		s.logger.WarnContext(ctx, "rca: shutdown deadline reached, cancelling in-flight generations")
		return ctx.Err()
	}
}

// rcaRetry calls fn up to maxAttempts times, waiting backoff(n) after the
// n-th failure. It stops early when ctx is done and returns the last error.
func rcaRetry[T any](
	ctx context.Context,
	maxAttempts int,
	backoff func(attempt int) time.Duration,
	fn func(ctx context.Context) (T, error),
) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, rcaStopped(err, lastErr, attempt-1)
		}
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		if err := rcaWait(ctx, backoff(attempt)); err != nil {
			return zero, rcaStopped(err, lastErr, attempt)
		}
	}
	return zero, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

// rcaStopped describes a retry loop cut short by ctx.
func rcaStopped(ctxErr, lastErr error, attempts int) error {
	if lastErr == nil {
		return fmt.Errorf("stopped before first attempt: %w", ctxErr)
	}
	return fmt.Errorf("stopped after %d attempts: %w; last error: %w", attempts, ctxErr, lastErr)
}

// rcaWait blocks for d, or until ctx is done.
func rcaWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
