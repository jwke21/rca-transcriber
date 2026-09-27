package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// transcriptionMaxAppendAttempts is how many times a finished transcript line
// is offered to the event repository before it is dropped.
const transcriptionMaxAppendAttempts = 3

// transcriptionDefaultAppendBackoff waits 100ms × attempt between Append attempts.
func transcriptionDefaultAppendBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * 100 * time.Millisecond
}

// TranscriptionService opens one speech-to-text stream per Twilio media stream
// and persists every final transcript line as soon as it arrives (FR3, NFR1).
type TranscriptionService struct {
	stt           port.SpeechToText
	events        port.IncidentEventRepository
	logger        *slog.Logger
	appendBackoff func(attempt int) time.Duration
}

var _ port.TranscriptionService = (*TranscriptionService)(nil)

// TranscriptionOption configures a TranscriptionService.
type TranscriptionOption func(*TranscriptionService)

// WithTranscriptionAppendBackoff sets the wait before retrying a failed Append.
// attempt is the 1-based number of the attempt that just failed. A nil func
// keeps the default of 100ms × attempt.
func WithTranscriptionAppendBackoff(backoff func(attempt int) time.Duration) TranscriptionOption {
	return func(s *TranscriptionService) {
		if backoff != nil {
			s.appendBackoff = backoff
		}
	}
}

// NewTranscriptionService returns a TranscriptionService. A nil logger discards
// log output.
func NewTranscriptionService(
	stt port.SpeechToText,
	events port.IncidentEventRepository,
	logger *slog.Logger,
	opts ...TranscriptionOption,
) *TranscriptionService {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &TranscriptionService{
		stt:           stt,
		events:        events,
		logger:        logger,
		appendBackoff: transcriptionDefaultAppendBackoff,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// StartSession opens speech-to-text for one media stream and starts persisting
// its results. The returned session must be ended with End, which releases the
// stream and the session's goroutines.
func (s *TranscriptionService) StartSession(ctx context.Context, incidentID, engineerID int64) (port.MediaSession, error) {
	stream, err := s.stt.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("transcription: open stream for incident %d: %w", incidentID, err)
	}

	// The session outlives the request that started it: persistence must carry
	// on until End, not until the webhook or WebSocket handler's ctx is done.
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sess := &transcriptionSession{
		stt:           s.stt,
		events:        s.events,
		appendBackoff: s.appendBackoff,
		logger:        s.logger.With("incident_id", incidentID, "engineer_id", engineerID),
		incidentID:    incidentID,
		engineerID:    engineerID,
		ctx:           sessionCtx,
		cancel:        cancel,
		stream:        stream,
		endDone:       make(chan struct{}),
	}
	sess.persisters.Add(1)
	go sess.persist(stream)

	sess.logger.InfoContext(ctx, "transcription: session started")
	return sess, nil
}

// transcriptionSession is the MediaSession for one live call.
//
// Goroutines: one persister per speech-to-text stream, tracked by persisters.
// A persister exits when its stream's Results channel closes, which happens on
// Finish or Close. End always closes the current stream and cancels ctx, and a
// stream replaced by a reopen is closed at the swap, so every persister exits.
type transcriptionSession struct {
	stt           port.SpeechToText
	events        port.IncidentEventRepository
	appendBackoff func(attempt int) time.Duration
	logger        *slog.Logger
	incidentID    int64
	engineerID    int64

	// ctx scopes the persisters' Append calls and retry waits. cancel is
	// called when End finishes or times out, so in-flight retries stop.
	ctx        context.Context
	cancel     context.CancelFunc
	persisters sync.WaitGroup

	mu       sync.Mutex
	stream   port.TranscriptionStream
	reopened bool
	degraded bool
	ended    bool

	endOnce sync.Once
	endDone chan struct{}
	endErr  error
}

var _ port.MediaSession = (*transcriptionSession)(nil)

// HandleAudio forwards one frame to speech-to-text. It does no database work.
// A failed send reopens the stream once per session; after that the session is
// degraded and audio is dropped so the call can continue untranscribed.
func (s *transcriptionSession) HandleAudio(ctx context.Context, mulaw []byte) error {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return domain.ErrSessionEnded
	}
	if s.degraded {
		s.mu.Unlock()
		return nil
	}
	stream := s.stream
	s.mu.Unlock()

	err := stream.SendAudio(mulaw)
	if err == nil {
		return nil
	}
	return s.reopen(ctx, stream, mulaw, err)
}

// reopen replaces the failed stream with a new one and resends the frame. It is
// attempted once per session; any further failure degrades the session.
func (s *transcriptionSession) reopen(ctx context.Context, failed port.TranscriptionStream, mulaw []byte, sendErr error) error {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return domain.ErrSessionEnded
	}
	if s.degraded {
		s.mu.Unlock()
		return nil
	}
	if s.reopened {
		s.degradeLocked(ctx, fmt.Errorf("reopened stream failed: %w", sendErr))
		s.mu.Unlock()
		return nil
	}
	s.reopened = true
	s.mu.Unlock()

	s.logger.WarnContext(ctx, "transcription: send audio failed, reopening stream", "error", sendErr)

	next, err := s.stt.Open(ctx)
	if err != nil {
		return s.degrade(ctx, fmt.Errorf("reopen stream: %w", err))
	}

	s.mu.Lock()
	if s.ended {
		// End has already taken the old stream; don't leak the new one.
		s.mu.Unlock()
		_ = next.Close()
		return domain.ErrSessionEnded
	}
	s.stream = next
	s.persisters.Add(1)
	s.mu.Unlock()
	go s.persist(next)

	// Close the failed stream so its Results channel closes and its persister
	// exits; otherwise End would wait on it until its ctx expired.
	if err := failed.Close(); err != nil {
		s.logger.DebugContext(ctx, "transcription: close failed stream", "error", err)
	}

	if err := next.SendAudio(mulaw); err != nil {
		return s.degrade(ctx, fmt.Errorf("resend audio on reopened stream: %w", err))
	}
	s.logger.InfoContext(ctx, "transcription: stream reopened")
	return nil
}

// degrade marks the session degraded unless it has already ended, in which
// case it reports domain.ErrSessionEnded.
func (s *transcriptionSession) degrade(ctx context.Context, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return domain.ErrSessionEnded
	}
	s.degradeLocked(ctx, cause)
	return nil
}

// degradeLocked marks the session degraded and logs once. s.mu must be held.
func (s *transcriptionSession) degradeLocked(ctx context.Context, cause error) {
	if s.degraded {
		return
	}
	s.degraded = true
	s.logger.ErrorContext(ctx, "transcription: session degraded, audio is no longer transcribed", "error", cause)
}

// HandleDTMF ends the session on "#", after every remaining line is saved.
// Other digits are ignored.
func (s *transcriptionSession) HandleDTMF(ctx context.Context, digit string) (bool, error) {
	if digit != "#" {
		return false, nil
	}
	return true, s.End(ctx)
}

// End flushes the stream, waits until every remaining line is saved (or ctx is
// done) and releases the session. It is idempotent and safe to call
// concurrently: the first caller does the work and the rest wait for it.
func (s *transcriptionSession) End(ctx context.Context) error {
	owner := false
	s.endOnce.Do(func() { owner = true })
	if owner {
		s.endErr = s.end(ctx)
		close(s.endDone)
		return s.endErr
	}
	select {
	case <-s.endDone:
		return s.endErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *transcriptionSession) end(ctx context.Context) error {
	s.mu.Lock()
	s.ended = true
	stream := s.stream
	s.mu.Unlock()

	defer func() {
		// Stop in-flight Append retries and make sure Results is closed so
		// every persister exits, whether we flushed or timed out.
		s.cancel()
		if err := stream.Close(); err != nil {
			s.logger.DebugContext(ctx, "transcription: close stream", "error", err)
		}
	}()

	// No new persisters can start once ended is set, so Wait is safe here.
	// This goroutine exits when Finish returns and the persisters are done,
	// which the deferred cancel and Close guarantee on timeout.
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		if err := stream.Finish(ctx); err != nil {
			s.logger.WarnContext(ctx, "transcription: finish stream", "error", err)
		}
		s.persisters.Wait()
	}()

	select {
	case <-flushed:
		s.logger.InfoContext(ctx, "transcription: session ended")
		return nil
	case <-ctx.Done():
		s.logger.ErrorContext(ctx, "transcription: session end timed out, remaining lines may be lost", "error", ctx.Err())
		return ctx.Err()
	}
}

// persist saves every final, non-blank segment from stream until its Results
// channel closes.
func (s *transcriptionSession) persist(stream port.TranscriptionStream) {
	defer s.persisters.Done()
	for seg := range stream.Results() {
		if !seg.IsFinal {
			continue
		}
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		s.save(domain.IncidentEvent{
			IncidentID:    s.incidentID,
			EngineerID:    s.engineerID,
			Transcription: text,
			CreatedAt:     seg.SpokenAt.UTC(),
		})
	}
}

// save appends event, retrying up to transcriptionMaxAppendAttempts times. On
// final failure the line is logged (without its text) and dropped.
func (s *transcriptionSession) save(event domain.IncidentEvent) {
	var err error
	for attempt := 1; attempt <= transcriptionMaxAppendAttempts; attempt++ {
		var saved domain.IncidentEvent
		saved, err = s.events.Append(s.ctx, event)
		if err == nil {
			s.logger.DebugContext(s.ctx, "transcription: line saved",
				"event_id", saved.ID, "spoken_at", event.CreatedAt, "text", event.Transcription)
			return
		}
		if attempt == transcriptionMaxAppendAttempts {
			break
		}
		s.logger.WarnContext(s.ctx, "transcription: save line failed, retrying", "attempt", attempt, "error", err)
		if !transcriptionWait(s.ctx, s.appendBackoff(attempt)) {
			err = fmt.Errorf("%w (retry cancelled: %w)", err, s.ctx.Err())
			break
		}
	}
	// s.logger carries incident_id and engineer_id.
	s.logger.ErrorContext(s.ctx, "transcription: dropped transcript line",
		"spoken_at", event.CreatedAt, "error", err)
}

// transcriptionWait waits for d, returning false if ctx is done first.
func transcriptionWait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
