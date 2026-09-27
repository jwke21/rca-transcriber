package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

const (
	rcaTestIncidentID = int64(4821)
	rcaTestPRURL      = "https://github.com/example/repo/pull/12"
)

var (
	rcaTestResolvedAt = time.Date(2026, 9, 27, 14, 25, 0, 0, time.UTC)
	rcaTestClockNow   = time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	rcaTestReport     = domain.RCAReport{
		Title:   "Payments API errors caused by ledger deploy",
		Summary: "The payments API returned errors after the ledger deploy. The deploy was rolled back.",
	}
	rcaTestErrBoom = errors.New("boom")
)

// rcaTestLogs is a goroutine-safe log sink.
type rcaTestLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rcaTestLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *rcaTestLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type rcaTestDeps struct {
	incidents *fakeIncidentRepository
	events    *fakeIncidentEventRepository
	generator *fakeRCAGenerator
	publisher *fakeRCAPublisher
	clock     *fakeClock
	logs      *rcaTestLogs
	svc       *RCAService
}

func rcaTestIncident() domain.Incident {
	resolvedAt := rcaTestResolvedAt
	return domain.Incident{
		ID:             rcaTestIncidentID,
		Status:         domain.IncidentStatusRCAGenerating,
		ResolutionTime: &resolvedAt,
		CreatedAt:      rcaTestResolvedAt.Add(-time.Hour),
	}
}

func rcaTestEvents() []domain.IncidentEvent {
	return []domain.IncidentEvent{
		{ID: 1, IncidentID: rcaTestIncidentID, EngineerID: 7, Transcription: "payments is erroring", CreatedAt: rcaTestResolvedAt.Add(-30 * time.Minute)},
		{ID: 2, IncidentID: rcaTestIncidentID, EngineerID: 7, Transcription: "rolled back the ledger deploy", CreatedAt: rcaTestResolvedAt.Add(-5 * time.Minute)},
	}
}

// rcaTestNew wires a service whose dependencies succeed by default, with zero
// backoff. Extra options are applied after the zero backoff.
func rcaTestNew(opts ...RCAOption) *rcaTestDeps {
	d := &rcaTestDeps{
		incidents: &fakeIncidentRepository{
			ClaimForGenerationFn: func(context.Context, int64) (bool, error) { return true, nil },
			GetFn:                func(context.Context, int64) (domain.Incident, error) { return rcaTestIncident(), nil },
		},
		events: &fakeIncidentEventRepository{
			ListByIncidentFn: func(context.Context, int64) ([]domain.IncidentEvent, error) { return rcaTestEvents(), nil },
		},
		generator: &fakeRCAGenerator{
			GenerateFn: func(context.Context, domain.Incident, []domain.IncidentEvent) (domain.RCAReport, error) {
				return rcaTestReport, nil
			},
		},
		publisher: &fakeRCAPublisher{
			PublishFn: func(context.Context, domain.RCADocument) (string, error) { return rcaTestPRURL, nil },
		},
		clock: newFakeClock(rcaTestClockNow),
		logs:  &rcaTestLogs{},
	}
	logger := slog.New(slog.NewTextHandler(d.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	all := append([]RCAOption{WithRCABackoff(func(int) time.Duration { return 0 })}, opts...)
	d.svc = NewRCAService(d.incidents, d.events, d.generator, d.publisher, d.clock, logger, all...)
	return d
}

func (d *rcaTestDeps) rcaTestStatuses() []setStatusCall {
	d.incidents.mu.Lock()
	defer d.incidents.mu.Unlock()
	return append([]setStatusCall(nil), d.incidents.setStatusCalls...)
}

func (d *rcaTestDeps) rcaTestPublished() []domain.RCADocument {
	d.publisher.mu.Lock()
	defer d.publisher.mu.Unlock()
	return append([]domain.RCADocument(nil), d.publisher.publishCalls...)
}

func (d *rcaTestDeps) rcaTestClaims() []int64 {
	d.incidents.mu.Lock()
	defer d.incidents.mu.Unlock()
	return append([]int64(nil), d.incidents.claimForGenerationCalls...)
}

func (d *rcaTestDeps) rcaTestGetCalls() int {
	d.incidents.mu.Lock()
	defer d.incidents.mu.Unlock()
	return len(d.incidents.getCalls)
}

func (d *rcaTestDeps) rcaTestListEventCalls() int {
	d.events.mu.Lock()
	defer d.events.mu.Unlock()
	return len(d.events.listByIncidentCalls)
}

func (d *rcaTestDeps) rcaTestShutdown(t *testing.T) {
	t.Helper()
	require.NoError(t, d.svc.Shutdown(context.Background()))
}

func rcaTestFinal(id int64, status domain.IncidentStatus) []setStatusCall {
	return []setStatusCall{{id: id, status: status}}
}

func TestNewRCAService_Defaults(t *testing.T) {
	svc := NewRCAService(nil, nil, nil, nil, nil, nil)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	require.NotNil(t, svc.logger)
	assert.Equal(t, 3, svc.maxAttempts)
	assert.Equal(t, 5*time.Minute, svc.generationTimeout)
	assert.Equal(t, time.Second, svc.backoff(1))
	assert.Equal(t, 2*time.Second, svc.backoff(2))
	assert.Equal(t, 4*time.Second, svc.backoff(3))
}

func TestNewRCAService_Options(t *testing.T) {
	custom := func(attempt int) time.Duration { return time.Duration(attempt) * time.Millisecond }
	tests := []struct {
		name        string
		opts        []RCAOption
		wantMax     int
		wantTimeout time.Duration
		wantBackoff time.Duration // backoff(2)
	}{
		{"custom values", []RCAOption{WithRCAMaxAttempts(5), WithRCAGenerationTimeout(time.Minute), WithRCABackoff(custom)}, 5, time.Minute, 2 * time.Millisecond},
		{"max attempts below one becomes one", []RCAOption{WithRCAMaxAttempts(0)}, 1, 5 * time.Minute, 2 * time.Second},
		{"non-positive timeout keeps default", []RCAOption{WithRCAGenerationTimeout(0), WithRCAGenerationTimeout(-time.Second)}, 3, 5 * time.Minute, 2 * time.Second},
		{"nil backoff and nil option keep defaults", []RCAOption{WithRCABackoff(nil), nil}, 3, 5 * time.Minute, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewRCAService(nil, nil, nil, nil, nil, nil, tt.opts...)
			t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
			assert.Equal(t, tt.wantMax, svc.maxAttempts)
			assert.Equal(t, tt.wantTimeout, svc.generationTimeout)
			assert.Equal(t, tt.wantBackoff, svc.backoff(2))
		})
	}
}

func TestRCADefaultBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, time.Second},
		{0, time.Second},
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{100, time.Second << 29},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, rcaDefaultBackoff(tt.attempt), "attempt %d", tt.attempt)
	}
}

func TestRCAService_Generate_HappyPath(t *testing.T) {
	d := rcaTestNew()
	var gotIncident domain.Incident
	var gotEvents []domain.IncidentEvent
	d.generator.GenerateFn = func(_ context.Context, incident domain.Incident, events []domain.IncidentEvent) (domain.RCAReport, error) {
		gotIncident, gotEvents = incident, events
		return rcaTestReport, nil
	}

	err := d.svc.Generate(context.Background(), rcaTestIncidentID)
	require.NoError(t, err)

	assert.Equal(t, []int64{rcaTestIncidentID}, d.rcaTestClaims())
	assert.Equal(t, 1, d.rcaTestGetCalls())
	assert.Equal(t, 1, d.rcaTestListEventCalls())
	assert.Equal(t, 1, d.generator.GenerateCalls())
	assert.Equal(t, rcaTestIncident(), gotIncident)
	assert.Equal(t, rcaTestEvents(), gotEvents)

	published := d.rcaTestPublished()
	require.Len(t, published, 1)
	doc := published[0]
	assert.Equal(t, rcaTestIncidentID, doc.IncidentID)
	assert.Equal(t, "incident-4821", doc.Branch)
	assert.Equal(t, "incidents/2026-09-27-4821.md", doc.Path)
	assert.Equal(t, "docs: incident 4821", doc.Title)
	assert.Equal(t, RenderRCAMarkdown(rcaTestIncidentID, rcaTestReport, rcaTestResolvedAt), doc.Content)
	assert.Contains(t, doc.Content, "# Incident 4821: Payments API errors caused by ledger deploy\n")
	assert.Equal(t, rcaPRBody(rcaTestIncidentID), doc.Body)

	assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAComplete), d.rcaTestStatuses())
	logs := d.logs.String()
	assert.Contains(t, logs, "level=INFO")
	assert.Contains(t, logs, rcaTestPRURL)
}

func TestRCAService_Generate_NotClaimed(t *testing.T) {
	d := rcaTestNew()
	d.incidents.ClaimForGenerationFn = func(context.Context, int64) (bool, error) { return false, nil }

	require.NoError(t, d.svc.Generate(context.Background(), rcaTestIncidentID))

	assert.Equal(t, []int64{rcaTestIncidentID}, d.rcaTestClaims())
	assert.Zero(t, d.rcaTestGetCalls())
	assert.Zero(t, d.rcaTestListEventCalls())
	assert.Zero(t, d.generator.GenerateCalls())
	assert.Empty(t, d.rcaTestPublished())
	assert.Empty(t, d.rcaTestStatuses())
}

func TestRCAService_Generate_ClaimError(t *testing.T) {
	d := rcaTestNew()
	d.incidents.ClaimForGenerationFn = func(context.Context, int64) (bool, error) { return false, rcaTestErrBoom }

	err := d.svc.Generate(context.Background(), rcaTestIncidentID)
	require.ErrorIs(t, err, rcaTestErrBoom)
	assert.Contains(t, err.Error(), "claim incident 4821")

	assert.Zero(t, d.rcaTestGetCalls())
	assert.Zero(t, d.generator.GenerateCalls())
	assert.Empty(t, d.rcaTestPublished())
	assert.Empty(t, d.rcaTestStatuses(), "nothing was claimed, so no status is written")
}

func TestRCAService_Generate_LoadFailures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(d *rcaTestDeps)
		wantErr error
		wantMsg string
	}{
		{
			name: "get incident error",
			setup: func(d *rcaTestDeps) {
				d.incidents.GetFn = func(context.Context, int64) (domain.Incident, error) { return domain.Incident{}, rcaTestErrBoom }
			},
			wantErr: rcaTestErrBoom,
			wantMsg: "get incident 4821",
		},
		{
			name: "get incident not found",
			setup: func(d *rcaTestDeps) {
				d.incidents.GetFn = func(context.Context, int64) (domain.Incident, error) { return domain.Incident{}, domain.ErrNotFound }
			},
			wantErr: domain.ErrNotFound,
			wantMsg: "get incident 4821",
		},
		{
			name: "list events error",
			setup: func(d *rcaTestDeps) {
				d.events.ListByIncidentFn = func(context.Context, int64) ([]domain.IncidentEvent, error) { return nil, rcaTestErrBoom }
			},
			wantErr: rcaTestErrBoom,
			wantMsg: "list transcript for incident 4821",
		},
		{
			name: "no events",
			setup: func(d *rcaTestDeps) {
				d.events.ListByIncidentFn = func(context.Context, int64) ([]domain.IncidentEvent, error) { return nil, nil }
			},
			wantErr: domain.ErrNoTranscript,
			wantMsg: "incident 4821",
		},
		{
			name: "empty event slice",
			setup: func(d *rcaTestDeps) {
				d.events.ListByIncidentFn = func(context.Context, int64) ([]domain.IncidentEvent, error) { return []domain.IncidentEvent{}, nil }
			},
			wantErr: domain.ErrNoTranscript,
			wantMsg: "incident 4821",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := rcaTestNew()
			tt.setup(d)

			err := d.svc.Generate(context.Background(), rcaTestIncidentID)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantMsg)

			assert.Zero(t, d.generator.GenerateCalls())
			assert.Empty(t, d.rcaTestPublished())
			assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
		})
	}
}

func TestRCAService_Generate_Retries(t *testing.T) {
	invalidTitle := domain.RCAReport{Title: "  \n", Summary: "Summary."}
	invalidSummary := domain.RCAReport{Title: "Title", Summary: " \t "}

	type result struct {
		report domain.RCAReport
		err    error
	}
	tests := []struct {
		name          string
		results       []result // one per generator call; the last repeats
		wantCalls     int
		wantPublished bool
		wantStatus    domain.IncidentStatus
		wantErrIs     []error
	}{
		{
			name:          "fails twice then succeeds",
			results:       []result{{err: rcaTestErrBoom}, {err: rcaTestErrBoom}, {report: rcaTestReport}},
			wantCalls:     3,
			wantPublished: true,
			wantStatus:    domain.IncidentStatusRCAComplete,
		},
		{
			name:          "blank title counts as a failed attempt",
			results:       []result{{report: invalidTitle}, {report: rcaTestReport}},
			wantCalls:     2,
			wantPublished: true,
			wantStatus:    domain.IncidentStatusRCAComplete,
		},
		{
			name:          "blank summary counts as a failed attempt",
			results:       []result{{report: invalidSummary}, {report: rcaTestReport}},
			wantCalls:     2,
			wantPublished: true,
			wantStatus:    domain.IncidentStatusRCAComplete,
		},
		{
			name:       "all attempts fail",
			results:    []result{{err: errors.New("first")}, {err: errors.New("second")}, {err: rcaTestErrBoom}},
			wantCalls:  3,
			wantStatus: domain.IncidentStatusRCAFailed,
			wantErrIs:  []error{rcaTestErrBoom},
		},
		{
			name:       "all attempts invalid",
			results:    []result{{report: invalidTitle}, {report: invalidSummary}},
			wantCalls:  3,
			wantStatus: domain.IncidentStatusRCAFailed,
			wantErrIs:  []error{domain.ErrInvalidRCA},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := rcaTestNew()
			var mu sync.Mutex
			call := 0
			d.generator.GenerateFn = func(context.Context, domain.Incident, []domain.IncidentEvent) (domain.RCAReport, error) {
				mu.Lock()
				defer mu.Unlock()
				r := tt.results[min(call, len(tt.results)-1)]
				call++
				return r.report, r.err
			}

			err := d.svc.Generate(context.Background(), rcaTestIncidentID)
			if len(tt.wantErrIs) == 0 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				for _, want := range tt.wantErrIs {
					assert.ErrorIs(t, err, want)
				}
				assert.Contains(t, err.Error(), "generate report for incident 4821")
			}

			assert.Equal(t, tt.wantCalls, d.generator.GenerateCalls())
			if tt.wantPublished {
				assert.Len(t, d.rcaTestPublished(), 1)
			} else {
				assert.Empty(t, d.rcaTestPublished(), "publisher must never be called when generation fails")
			}
			assert.Equal(t, rcaTestFinal(rcaTestIncidentID, tt.wantStatus), d.rcaTestStatuses())
		})
	}
}

func TestRCAService_Generate_BackoffBetweenAttempts(t *testing.T) {
	var mu sync.Mutex
	var waits []int
	d := rcaTestNew(WithRCABackoff(func(attempt int) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, attempt)
		return time.Nanosecond
	}))
	d.generator.GenerateFn = func(context.Context, domain.Incident, []domain.IncidentEvent) (domain.RCAReport, error) {
		return domain.RCAReport{}, rcaTestErrBoom
	}

	err := d.svc.Generate(context.Background(), rcaTestIncidentID)
	require.ErrorIs(t, err, rcaTestErrBoom)
	assert.Contains(t, err.Error(), "after 3 attempts")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{1, 2}, waits, "backoff runs between attempts, not after the last one")
}

func TestRCAService_Generate_PublishFailures(t *testing.T) {
	t.Run("fails on every attempt", func(t *testing.T) {
		d := rcaTestNew()
		d.publisher.PublishFn = func(context.Context, domain.RCADocument) (string, error) { return "", rcaTestErrBoom }

		err := d.svc.Generate(context.Background(), rcaTestIncidentID)
		require.ErrorIs(t, err, rcaTestErrBoom)
		assert.Contains(t, err.Error(), "publish report for incident 4821")

		assert.Equal(t, 1, d.generator.GenerateCalls())
		assert.Len(t, d.rcaTestPublished(), 3)
		assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
	})

	t.Run("fails once then succeeds", func(t *testing.T) {
		d := rcaTestNew()
		var mu sync.Mutex
		calls := 0
		d.publisher.PublishFn = func(context.Context, domain.RCADocument) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return "", rcaTestErrBoom
			}
			return rcaTestPRURL, nil
		}

		require.NoError(t, d.svc.Generate(context.Background(), rcaTestIncidentID))
		published := d.rcaTestPublished()
		require.Len(t, published, 2)
		assert.Equal(t, published[0], published[1], "the same document is retried")
		assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAComplete), d.rcaTestStatuses())
	})
}

func TestRCAService_Generate_ContextCancellation(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(d *rcaTestDeps, cancel context.CancelFunc) []RCAOption
		preCancel bool
		wantCalls int
		wantErrIs []error
	}{
		{
			name:      "already cancelled before the first attempt",
			preCancel: true,
			wantCalls: 0,
			wantErrIs: []error{context.Canceled},
		},
		{
			name: "cancelled during an attempt stops retries",
			setup: func(d *rcaTestDeps, cancel context.CancelFunc) []RCAOption {
				d.generator.GenerateFn = func(context.Context, domain.Incident, []domain.IncidentEvent) (domain.RCAReport, error) {
					cancel()
					return domain.RCAReport{}, rcaTestErrBoom
				}
				return nil
			},
			wantCalls: 1,
			wantErrIs: []error{context.Canceled, rcaTestErrBoom},
		},
		{
			name: "cancelled during the backoff wait stops retries",
			setup: func(d *rcaTestDeps, cancel context.CancelFunc) []RCAOption {
				d.generator.GenerateFn = func(context.Context, domain.Incident, []domain.IncidentEvent) (domain.RCAReport, error) {
					return domain.RCAReport{}, rcaTestErrBoom
				}
				return []RCAOption{WithRCABackoff(func(int) time.Duration {
					cancel()
					return time.Hour
				})}
			},
			wantCalls: 1,
			wantErrIs: []error{context.Canceled, rcaTestErrBoom},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			d := rcaTestNew()
			if tt.setup != nil {
				if opts := tt.setup(d, cancel); opts != nil {
					for _, opt := range opts {
						opt(d.svc)
					}
				}
			}
			var statusCtxErr error
			d.incidents.SetStatusFn = func(ctx context.Context, _ int64, _ domain.IncidentStatus) error {
				statusCtxErr = ctx.Err()
				return nil
			}
			if tt.preCancel {
				cancel()
			}

			err := d.svc.Generate(ctx, rcaTestIncidentID)
			require.Error(t, err)
			for _, want := range tt.wantErrIs {
				assert.ErrorIs(t, err, want)
			}

			assert.Equal(t, tt.wantCalls, d.generator.GenerateCalls())
			assert.Empty(t, d.rcaTestPublished())
			assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
			assert.NoError(t, statusCtxErr, "the status is written with a fresh context")
		})
	}
}

func TestRCAService_Generate_CancelledDuringPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := rcaTestNew()
	d.publisher.PublishFn = func(context.Context, domain.RCADocument) (string, error) {
		cancel()
		return "", rcaTestErrBoom
	}

	err := d.svc.Generate(ctx, rcaTestIncidentID)
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, rcaTestErrBoom)
	assert.Len(t, d.rcaTestPublished(), 1)
	assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
}

func TestRCAService_Generate_StatusWriteFailureIsLogged(t *testing.T) {
	d := rcaTestNew()
	d.incidents.SetStatusFn = func(context.Context, int64, domain.IncidentStatus) error { return rcaTestErrBoom }

	require.NoError(t, d.svc.Generate(context.Background(), rcaTestIncidentID))
	assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAComplete), d.rcaTestStatuses())
	logs := d.logs.String()
	assert.Contains(t, logs, "set final status failed")
	assert.Contains(t, logs, "boom")
}

func TestRCAService_Generate_NilResolutionTimeUsesClock(t *testing.T) {
	d := rcaTestNew()
	d.incidents.GetFn = func(context.Context, int64) (domain.Incident, error) {
		incident := rcaTestIncident()
		incident.ResolutionTime = nil
		return incident, nil
	}

	require.NoError(t, d.svc.Generate(context.Background(), rcaTestIncidentID))

	published := d.rcaTestPublished()
	require.Len(t, published, 1)
	assert.Equal(t, "incidents/2026-10-03-4821.md", published[0].Path)
	assert.Contains(t, published[0].Content, "**Date:** 2026-10-03\n")
	logs := d.logs.String()
	assert.Contains(t, logs, "level=WARN")
	assert.Contains(t, logs, "no resolution time")
}

func TestRCAService_Enqueue_RunsAsynchronously(t *testing.T) {
	d := rcaTestNew(WithRCAGenerationTimeout(time.Hour))
	started := make(chan struct{})
	release := make(chan struct{})
	var hadDeadline bool
	d.generator.GenerateFn = func(ctx context.Context, _ domain.Incident, _ []domain.IncidentEvent) (domain.RCAReport, error) {
		_, hadDeadline = ctx.Deadline()
		close(started)
		<-release
		return rcaTestReport, nil
	}

	d.svc.Enqueue(rcaTestIncidentID) // must return while the generator is blocked
	<-started
	assert.Empty(t, d.rcaTestPublished(), "generation is still in flight")
	close(release)

	d.rcaTestShutdown(t)
	assert.True(t, hadDeadline, "background generation runs with the generation timeout")
	assert.Len(t, d.rcaTestPublished(), 1)
	assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAComplete), d.rcaTestStatuses())
}

func TestRCAService_Enqueue_LogsFailure(t *testing.T) {
	d := rcaTestNew()
	d.events.ListByIncidentFn = func(context.Context, int64) ([]domain.IncidentEvent, error) { return nil, nil }

	d.svc.Enqueue(rcaTestIncidentID)
	d.rcaTestShutdown(t)

	assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
	logs := d.logs.String()
	assert.Contains(t, logs, "background generation failed")
	assert.Contains(t, logs, domain.ErrNoTranscript.Error())
}

func TestRCAService_Enqueue_AfterShutdownIsNoop(t *testing.T) {
	d := rcaTestNew()
	d.rcaTestShutdown(t)

	d.svc.Enqueue(rcaTestIncidentID)
	d.rcaTestShutdown(t) // would wait for any goroutine Enqueue had started

	assert.Empty(t, d.rcaTestClaims())
	assert.Zero(t, d.generator.GenerateCalls())
	assert.Empty(t, d.rcaTestStatuses())
	logs := d.logs.String()
	assert.Contains(t, logs, "level=WARN")
	assert.Contains(t, logs, "shutting down")
}

func TestRCAService_Shutdown_Idle(t *testing.T) {
	d := rcaTestNew()
	require.NoError(t, d.svc.Shutdown(context.Background()))
	require.NoError(t, d.svc.Shutdown(context.Background()), "safe to call more than once")
}

func TestRCAService_Shutdown_ExpiredContextCancelsInFlight(t *testing.T) {
	tests := []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(0, 0))
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := rcaTestNew()
			started := make(chan struct{})
			var generatorErr error
			d.generator.GenerateFn = func(ctx context.Context, _ domain.Incident, _ []domain.IncidentEvent) (domain.RCAReport, error) {
				close(started)
				<-ctx.Done()
				generatorErr = ctx.Err()
				return domain.RCAReport{}, ctx.Err()
			}

			d.svc.Enqueue(rcaTestIncidentID)
			<-started

			ctx, cancel := tt.ctx()
			defer cancel()
			err := d.svc.Shutdown(ctx)
			require.ErrorIs(t, err, tt.wantErr)

			// A second Shutdown waits for the aborted generation to finish.
			d.rcaTestShutdown(t)
			assert.ErrorIs(t, generatorErr, context.Canceled)
			assert.Equal(t, 1, d.generator.GenerateCalls(), "no retries after cancellation")
			assert.Empty(t, d.rcaTestPublished())
			assert.Equal(t, rcaTestFinal(rcaTestIncidentID, domain.IncidentStatusRCAFailed), d.rcaTestStatuses())
			assert.Contains(t, d.logs.String(), "shutdown deadline reached")
		})
	}
}

func TestRCAService_Shutdown_ConcurrentWithEnqueue(t *testing.T) {
	d := rcaTestNew()
	d.incidents.ClaimForGenerationFn = func(context.Context, int64) (bool, error) { return false, nil }

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.svc.Enqueue(int64(i + 1))
		}()
	}
	require.NoError(t, d.svc.Shutdown(context.Background()))
	wg.Wait()
	// Whatever was accepted before Shutdown has finished; later calls were rejected.
	d.rcaTestShutdown(t)
	assert.LessOrEqual(t, len(d.rcaTestClaims()), 20)
}

func TestRCAService_RecoverPending(t *testing.T) {
	d := rcaTestNew()
	d.incidents.ClaimForGenerationFn = func(context.Context, int64) (bool, error) { return false, nil }
	d.incidents.ListByStatusFn = func(_ context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error) {
		require.Len(t, statuses, 1)
		switch statuses[0] {
		case domain.IncidentStatusRCAGenerating:
			return []domain.Incident{{ID: 1}, {ID: 2}}, nil
		case domain.IncidentStatusRCAPending:
			return []domain.Incident{{ID: 1}, {ID: 2}, {ID: 3}}, nil
		}
		return nil, errors.New("unexpected status")
	}

	require.NoError(t, d.svc.RecoverPending(context.Background()))
	d.rcaTestShutdown(t)

	d.incidents.mu.Lock()
	listCalls := append([][]domain.IncidentStatus(nil), d.incidents.listByStatusCalls...)
	d.incidents.mu.Unlock()
	assert.Equal(t, [][]domain.IncidentStatus{
		{domain.IncidentStatusRCAGenerating},
		{domain.IncidentStatusRCAPending},
	}, listCalls, "generating is reset before pending is listed")
	assert.Equal(t, []setStatusCall{
		{id: 1, status: domain.IncidentStatusRCAPending},
		{id: 2, status: domain.IncidentStatusRCAPending},
	}, d.rcaTestStatuses())
	assert.ElementsMatch(t, []int64{1, 2, 3}, d.rcaTestClaims(), "every pending incident is enqueued")
}

func TestRCAService_RecoverPending_Errors(t *testing.T) {
	errListGenerating := errors.New("list generating failed")
	errListPending := errors.New("list pending failed")
	errSetStatus := errors.New("set status failed")

	tests := []struct {
		name           string
		listGenerating error
		listPending    error
		setStatusFails map[int64]bool
		wantErrs       []error
		wantEnqueued   []int64
		wantSetStatus  []setStatusCall
	}{
		{
			name:           "list generating fails, pending still enqueued",
			listGenerating: errListGenerating,
			wantErrs:       []error{errListGenerating},
			wantEnqueued:   []int64{3},
		},
		{
			name:           "set status fails for one, the rest continue",
			setStatusFails: map[int64]bool{1: true},
			wantErrs:       []error{errSetStatus},
			wantEnqueued:   []int64{3},
			wantSetStatus: []setStatusCall{
				{id: 1, status: domain.IncidentStatusRCAPending},
				{id: 2, status: domain.IncidentStatusRCAPending},
			},
		},
		{
			name:           "errors from list and set status are joined",
			setStatusFails: map[int64]bool{1: true, 2: true},
			listPending:    errListPending,
			wantErrs:       []error{errSetStatus, errListPending},
			wantSetStatus: []setStatusCall{
				{id: 1, status: domain.IncidentStatusRCAPending},
				{id: 2, status: domain.IncidentStatusRCAPending},
			},
		},
		{
			name:           "both lists fail",
			listGenerating: errListGenerating,
			listPending:    errListPending,
			wantErrs:       []error{errListGenerating, errListPending},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := rcaTestNew()
			d.incidents.ClaimForGenerationFn = func(context.Context, int64) (bool, error) { return false, nil }
			d.incidents.ListByStatusFn = func(_ context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error) {
				if statuses[0] == domain.IncidentStatusRCAGenerating {
					if tt.listGenerating != nil {
						return nil, tt.listGenerating
					}
					return []domain.Incident{{ID: 1}, {ID: 2}}, nil
				}
				if tt.listPending != nil {
					return nil, tt.listPending
				}
				return []domain.Incident{{ID: 3}}, nil
			}
			d.incidents.SetStatusFn = func(_ context.Context, id int64, _ domain.IncidentStatus) error {
				if tt.setStatusFails[id] {
					return errSetStatus
				}
				return nil
			}

			err := d.svc.RecoverPending(context.Background())
			d.rcaTestShutdown(t)

			require.Error(t, err)
			for _, want := range tt.wantErrs {
				assert.ErrorIs(t, err, want)
			}
			assert.ElementsMatch(t, tt.wantEnqueued, d.rcaTestClaims())
			assert.Equal(t, tt.wantSetStatus, d.rcaTestStatuses())
		})
	}
}
