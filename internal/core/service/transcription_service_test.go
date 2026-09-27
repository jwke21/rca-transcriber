package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

const (
	transcriptionTestIncidentID = int64(4821)
	transcriptionTestEngineerID = int64(7)
	transcriptionTestWait       = 2 * time.Second
	transcriptionTestTick       = time.Millisecond
)

var (
	transcriptionTestErrDB   = errors.New("db exploded")
	transcriptionTestErrSend = errors.New("websocket broken")
	transcriptionTestErrOpen = errors.New("dial failed")
	transcriptionTestZone    = time.FixedZone("PDT", -7*60*60)
	transcriptionTestSpoken  = time.Date(2026, 9, 27, 7, 30, 0, 0, transcriptionTestZone)
)

// transcriptionTestLogs is a goroutine-safe log sink: persisters log from
// their own goroutines.
type transcriptionTestLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *transcriptionTestLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *transcriptionTestLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// transcriptionTestDeps bundles the fakes behind one TranscriptionService.
type transcriptionTestDeps struct {
	stt    *fakeSpeechToText
	events *fakeIncidentEventRepository
	logs   *transcriptionTestLogs
	svc    *TranscriptionService

	mu      sync.Mutex
	streams []*fakeTranscriptionStream // handed out by Open, in order
	saved   []domain.IncidentEvent     // successful Appends only
}

// transcriptionTestNew builds a service with zero append backoff whose Open
// hands out streams in order and fails once they run out. The logger runs at
// Info level, so any transcript text in the logs is a bug.
func transcriptionTestNew(streams ...*fakeTranscriptionStream) *transcriptionTestDeps {
	d := &transcriptionTestDeps{
		stt:     &fakeSpeechToText{},
		events:  &fakeIncidentEventRepository{},
		logs:    &transcriptionTestLogs{},
		streams: streams,
	}
	d.stt.OpenFn = func(context.Context) (port.TranscriptionStream, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(d.streams) == 0 {
			return nil, transcriptionTestErrOpen
		}
		next := d.streams[0]
		d.streams = d.streams[1:]
		return next, nil
	}
	d.transcriptionTestSetAppend(nil)
	logger := slog.New(slog.NewTextHandler(d.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d.svc = NewTranscriptionService(d.stt, d.events, logger,
		WithTranscriptionAppendBackoff(func(int) time.Duration { return 0 }))
	return d
}

// transcriptionTestSetAppend installs an Append that records successes. fail,
// if set, decides per call whether to return an error.
func (d *transcriptionTestDeps) transcriptionTestSetAppend(fail func(event domain.IncidentEvent) error) {
	d.events.AppendFn = func(_ context.Context, event domain.IncidentEvent) (domain.IncidentEvent, error) {
		if fail != nil {
			if err := fail(event); err != nil {
				return domain.IncidentEvent{}, err
			}
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.saved = append(d.saved, event)
		event.ID = int64(len(d.saved))
		return event, nil
	}
}

func (d *transcriptionTestDeps) transcriptionTestSaved() []domain.IncidentEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]domain.IncidentEvent(nil), d.saved...)
}

func (d *transcriptionTestDeps) transcriptionTestSavedTexts() []string {
	var texts []string
	for _, e := range d.transcriptionTestSaved() {
		texts = append(texts, e.Transcription)
	}
	return texts
}

func (d *transcriptionTestDeps) transcriptionTestAppendCalls() int {
	d.events.mu.Lock()
	defer d.events.mu.Unlock()
	return len(d.events.appendCalls)
}

func (d *transcriptionTestDeps) transcriptionTestOpenCalls() int {
	d.stt.mu.Lock()
	defer d.stt.mu.Unlock()
	return d.stt.openCalls
}

// transcriptionTestStart starts a session and registers a cleanup that ends it,
// so no test leaks persister goroutines.
func (d *transcriptionTestDeps) transcriptionTestStart(t *testing.T) port.MediaSession {
	t.Helper()
	sess, err := d.svc.StartSession(context.Background(), transcriptionTestIncidentID, transcriptionTestEngineerID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), transcriptionTestWait)
		defer cancel()
		_ = sess.End(ctx)
	})
	return sess
}

func transcriptionTestSeg(text string, offset time.Duration) domain.TranscriptSegment {
	return domain.TranscriptSegment{
		Text:     text,
		SpokenAt: transcriptionTestSpoken.Add(offset),
		Duration: time.Second,
		IsFinal:  true,
	}
}

// transcriptionTestEnd ends the session with a generous deadline.
func transcriptionTestEnd(t *testing.T, sess port.MediaSession) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), transcriptionTestWait)
	defer cancel()
	return sess.End(ctx)
}

// transcriptionTestResultsClosed reports whether an empty stream's Results
// channel is closed, without blocking.
func transcriptionTestResultsClosed(s *fakeTranscriptionStream) bool {
	select {
	case _, ok := <-s.Results():
		return !ok
	default:
		return false
	}
}

func transcriptionTestAssertNoTranscriptInLogs(t *testing.T, d *transcriptionTestDeps, texts ...string) {
	t.Helper()
	logs := d.logs.String()
	for _, text := range texts {
		assert.NotContains(t, logs, text, "transcript text must not be logged above debug")
	}
}

func TestNewTranscriptionService(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		svc := NewTranscriptionService(&fakeSpeechToText{}, &fakeIncidentEventRepository{}, nil)
		require.NotNil(t, svc.logger)
		assert.Equal(t, 100*time.Millisecond, svc.appendBackoff(1))
		assert.Equal(t, 200*time.Millisecond, svc.appendBackoff(2))
	})

	t.Run("nil backoff option keeps the default", func(t *testing.T) {
		svc := NewTranscriptionService(&fakeSpeechToText{}, &fakeIncidentEventRepository{}, nil,
			WithTranscriptionAppendBackoff(nil))
		assert.Equal(t, 300*time.Millisecond, svc.appendBackoff(3))
	})

	t.Run("custom backoff", func(t *testing.T) {
		svc := NewTranscriptionService(&fakeSpeechToText{}, &fakeIncidentEventRepository{}, nil,
			WithTranscriptionAppendBackoff(func(attempt int) time.Duration { return time.Duration(attempt) }))
		assert.Equal(t, time.Duration(2), svc.appendBackoff(2))
	})
}

func TestTranscriptionService_StartSession(t *testing.T) {
	t.Run("open failure returns a wrapped error", func(t *testing.T) {
		d := transcriptionTestNew() // no streams: Open fails
		sess, err := d.svc.StartSession(context.Background(), transcriptionTestIncidentID, transcriptionTestEngineerID)
		require.Error(t, err)
		assert.ErrorIs(t, err, transcriptionTestErrOpen)
		assert.Contains(t, err.Error(), "4821")
		assert.Nil(t, sess)
	})

	t.Run("persistence outlives the request context", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		appendCtxErr := make(chan error, 1)
		d.transcriptionTestSetAppend(func(domain.IncidentEvent) error { return nil })
		inner := d.events.AppendFn
		d.events.AppendFn = func(ctx context.Context, e domain.IncidentEvent) (domain.IncidentEvent, error) {
			appendCtxErr <- ctx.Err()
			return inner(ctx, e)
		}

		reqCtx, cancelReq := context.WithCancel(context.Background())
		sess, err := d.svc.StartSession(reqCtx, transcriptionTestIncidentID, transcriptionTestEngineerID)
		require.NoError(t, err)
		cancelReq() // the webhook/WebSocket request is done

		stream.Emit(transcriptionTestSeg("still listening", 0))
		require.NoError(t, transcriptionTestEnd(t, sess))

		assert.NoError(t, <-appendCtxErr, "Append must not see the request's cancellation")
		assert.Equal(t, []string{"still listening"}, d.transcriptionTestSavedTexts())
	})
}

func TestTranscriptionSession_Persist(t *testing.T) {
	stream := newFakeTranscriptionStream()
	d := transcriptionTestNew(stream)
	sess := d.transcriptionTestStart(t)

	interim := transcriptionTestSeg("check the dash", 0)
	interim.IsFinal = false
	stream.Emit(transcriptionTestSeg("  checking the payments dashboard  ", 0))
	stream.Emit(interim)
	stream.Emit(transcriptionTestSeg("   \t\n", 2*time.Second))
	stream.Emit(transcriptionTestSeg("", 3*time.Second))
	stream.Emit(transcriptionTestSeg("error rate is at forty percent", 4*time.Second))
	stream.Emit(transcriptionTestSeg("rolling back the ledger deploy", 9*time.Second))

	require.NoError(t, transcriptionTestEnd(t, sess))

	want := []domain.IncidentEvent{
		{Transcription: "checking the payments dashboard", CreatedAt: time.Date(2026, 9, 27, 14, 30, 0, 0, time.UTC)},
		{Transcription: "error rate is at forty percent", CreatedAt: time.Date(2026, 9, 27, 14, 30, 4, 0, time.UTC)},
		{Transcription: "rolling back the ledger deploy", CreatedAt: time.Date(2026, 9, 27, 14, 30, 9, 0, time.UTC)},
	}
	got := d.transcriptionTestSaved()
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, transcriptionTestIncidentID, got[i].IncidentID)
		assert.Equal(t, transcriptionTestEngineerID, got[i].EngineerID)
		assert.Equal(t, int64(0), got[i].ID, "event.ID is assigned by the repository")
		assert.Equal(t, want[i].Transcription, got[i].Transcription)
		assert.True(t, want[i].CreatedAt.Equal(got[i].CreatedAt))
		assert.Equal(t, time.UTC, got[i].CreatedAt.Location(), "CreatedAt must be UTC")
	}
	assert.Equal(t, 3, d.transcriptionTestAppendCalls(), "blank and non-final segments are not appended")
	transcriptionTestAssertNoTranscriptInLogs(t, d, "payments dashboard", "forty percent", "ledger deploy")
}

func TestTranscriptionSession_HandleDTMF(t *testing.T) {
	t.Run("# flushes and saves segments emitted during Finish before returning", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		stream.FinishFn = func(context.Context) error {
			// Deepgram's Finalize + CloseStream delivers the last words now.
			stream.Emit(transcriptionTestSeg("and the rollback", 0))
			stream.Emit(transcriptionTestSeg("fixed it", time.Second))
			stream.CloseResults()
			return nil
		}
		sess := d.transcriptionTestStart(t)
		stream.Emit(transcriptionTestSeg("rolling back now", -time.Second))

		ctx, cancel := context.WithTimeout(context.Background(), transcriptionTestWait)
		defer cancel()
		endStream, err := sess.HandleDTMF(ctx, "#")

		require.NoError(t, err)
		assert.True(t, endStream)
		assert.Equal(t, []string{"rolling back now", "and the rollback", "fixed it"}, d.transcriptionTestSavedTexts(),
			"every line must be saved before HandleDTMF returns")
		assert.ErrorIs(t, sess.HandleAudio(ctx, []byte{0xff}), domain.ErrSessionEnded)
	})

	t.Run("# returns End's error", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		stream.FinishFn = func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}
		sess := d.transcriptionTestStart(t)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		endStream, err := sess.HandleDTMF(ctx, "#")

		assert.True(t, endStream)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	for _, digit := range []string{"1", "0", "*", "9", ""} {
		t.Run(fmt.Sprintf("digit %q is ignored", digit), func(t *testing.T) {
			stream := newFakeTranscriptionStream()
			finished := false
			stream.FinishFn = func(context.Context) error {
				finished = true
				stream.CloseResults()
				return nil
			}
			d := transcriptionTestNew(stream)
			sess := d.transcriptionTestStart(t)

			endStream, err := sess.HandleDTMF(context.Background(), digit)

			require.NoError(t, err)
			assert.False(t, endStream)
			require.NoError(t, sess.HandleAudio(context.Background(), []byte{1}))
			assert.Len(t, stream.SentAudio(), 1, "session keeps streaming")
			require.NoError(t, transcriptionTestEnd(t, sess))
			assert.True(t, finished, "only End finishes the stream")
		})
	}
}

func TestTranscriptionSession_AppendRetries(t *testing.T) {
	tests := []struct {
		name        string
		failures    int // consecutive Append failures for the first line
		wantSaved   []string
		wantCalls   int
		wantDropLog bool
	}{
		{name: "succeeds first time", failures: 0, wantSaved: []string{"line one", "line two"}, wantCalls: 2},
		{name: "fails twice then succeeds", failures: 2, wantSaved: []string{"line one", "line two"}, wantCalls: 4},
		{name: "fails three times and is dropped", failures: 3, wantSaved: []string{"line two"}, wantCalls: 4, wantDropLog: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newFakeTranscriptionStream()
			d := transcriptionTestNew(stream)
			var mu sync.Mutex
			remaining := tt.failures
			d.transcriptionTestSetAppend(func(e domain.IncidentEvent) error {
				mu.Lock()
				defer mu.Unlock()
				if e.Transcription == "line one" && remaining > 0 {
					remaining--
					return transcriptionTestErrDB
				}
				return nil
			})
			sess := d.transcriptionTestStart(t)

			stream.Emit(transcriptionTestSeg("line one", 0))
			require.Eventually(t, func() bool {
				return d.transcriptionTestAppendCalls() >= min(tt.failures+1, 3)
			}, transcriptionTestWait, transcriptionTestTick)

			// The session is still usable after a dropped line.
			require.NoError(t, sess.HandleAudio(context.Background(), []byte{1, 2}))
			stream.Emit(transcriptionTestSeg("line two", time.Second))
			require.NoError(t, transcriptionTestEnd(t, sess))

			assert.Equal(t, tt.wantSaved, d.transcriptionTestSavedTexts())
			assert.Equal(t, tt.wantCalls, d.transcriptionTestAppendCalls())
			logs := d.logs.String()
			if tt.wantDropLog {
				assert.Contains(t, logs, "dropped transcript line")
				assert.Contains(t, logs, "level=ERROR")
				assert.Contains(t, logs, "incident_id=4821")
				assert.Contains(t, logs, transcriptionTestErrDB.Error())
			} else {
				assert.NotContains(t, logs, "dropped transcript line")
			}
			transcriptionTestAssertNoTranscriptInLogs(t, d, "line one", "line two")
		})
	}
}

func TestTranscriptionSession_AppendBackoff(t *testing.T) {
	t.Run("waits the backoff between attempts", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		var mu sync.Mutex
		var attempts []int
		d.svc.appendBackoff = func(attempt int) time.Duration {
			mu.Lock()
			defer mu.Unlock()
			attempts = append(attempts, attempt)
			return time.Microsecond
		}
		d.transcriptionTestSetAppend(func(domain.IncidentEvent) error { return transcriptionTestErrDB })
		sess := d.transcriptionTestStart(t)

		stream.Emit(transcriptionTestSeg("line", 0))
		require.NoError(t, transcriptionTestEnd(t, sess))

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []int{1, 2}, attempts, "no wait after the final attempt")
		assert.Equal(t, 3, d.transcriptionTestAppendCalls())
	})

	t.Run("End cancels in-flight retries", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		d.svc.appendBackoff = func(int) time.Duration { return time.Hour }
		d.transcriptionTestSetAppend(func(domain.IncidentEvent) error { return transcriptionTestErrDB })
		sess := d.transcriptionTestStart(t)

		stream.Emit(transcriptionTestSeg("line", 0))
		require.Eventually(t, func() bool { return d.transcriptionTestAppendCalls() == 1 },
			transcriptionTestWait, transcriptionTestTick)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := sess.End(ctx)

		assert.ErrorIs(t, err, context.DeadlineExceeded, "End waits for persisters until its ctx expires")
		require.Eventually(t, func() bool {
			return strings.Contains(d.logs.String(), "retry cancelled")
		}, transcriptionTestWait, transcriptionTestTick, "the persister must stop retrying once End gives up")
		assert.Equal(t, 1, d.transcriptionTestAppendCalls())
	})

	t.Run("zero backoff stops when the session is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		assert.True(t, transcriptionWait(ctx, 0))
		cancel()
		assert.False(t, transcriptionWait(ctx, 0))
		assert.False(t, transcriptionWait(ctx, time.Hour))
	})
}

func TestTranscriptionSession_Reopen(t *testing.T) {
	t.Run("send failure reopens once, resends the frame and saves the new stream's lines", func(t *testing.T) {
		first := newFakeTranscriptionStream()
		first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
		second := newFakeTranscriptionStream()
		d := transcriptionTestNew(first, second)
		sess := d.transcriptionTestStart(t)
		first.Emit(transcriptionTestSeg("before the drop", 0))
		require.Eventually(t, func() bool { return len(d.transcriptionTestSaved()) == 1 },
			transcriptionTestWait, transcriptionTestTick)

		frame := []byte{0x7f, 0x80, 0x81}
		require.NoError(t, sess.HandleAudio(context.Background(), frame))

		assert.Equal(t, 2, d.transcriptionTestOpenCalls(), "exactly one reopen")
		assert.Equal(t, [][]byte{frame}, second.SentAudio(), "the failed frame is resent")
		assert.True(t, transcriptionTestResultsClosed(first), "the failed stream is closed")

		require.NoError(t, sess.HandleAudio(context.Background(), []byte{1}))
		assert.Len(t, second.SentAudio(), 2, "later frames go to the new stream")
		assert.Len(t, first.SentAudio(), 1)

		second.Emit(transcriptionTestSeg("after the reopen", 5*time.Second))
		require.NoError(t, transcriptionTestEnd(t, sess), "End must not wait on the failed stream's persister")

		assert.Equal(t, []string{"before the drop", "after the reopen"}, d.transcriptionTestSavedTexts())
		assert.Equal(t, 2, d.transcriptionTestOpenCalls())
		assert.Contains(t, d.logs.String(), "stream reopened")
	})
}

func TestTranscriptionSession_Degraded(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the streams Open hands out, in order. A third stream,
		// where present, must never be opened.
		setup func() []*fakeTranscriptionStream
	}{
		{
			name: "reopen fails",
			setup: func() []*fakeTranscriptionStream {
				first := newFakeTranscriptionStream()
				first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
				return []*fakeTranscriptionStream{first} // second Open fails
			},
		},
		{
			name: "resend on the reopened stream fails",
			setup: func() []*fakeTranscriptionStream {
				first := newFakeTranscriptionStream()
				first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
				second := newFakeTranscriptionStream()
				second.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
				return []*fakeTranscriptionStream{first, second, newFakeTranscriptionStream()}
			},
		},
		{
			name: "reopened stream fails later",
			setup: func() []*fakeTranscriptionStream {
				first := newFakeTranscriptionStream()
				first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
				second := newFakeTranscriptionStream()
				var mu sync.Mutex
				sends := 0
				second.SendAudioFn = func([]byte) error {
					mu.Lock()
					defer mu.Unlock()
					sends++
					if sends > 2 {
						return transcriptionTestErrSend
					}
					return nil
				}
				return []*fakeTranscriptionStream{first, second, newFakeTranscriptionStream()}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams := tt.setup()
			d := transcriptionTestNew(streams...)
			sess := d.transcriptionTestStart(t)

			for i := 0; i < 6; i++ {
				require.NoError(t, sess.HandleAudio(context.Background(), []byte{byte(i)}),
					"a degraded session keeps the call going")
			}

			assert.LessOrEqual(t, d.transcriptionTestOpenCalls(), 2, "no further opens once degraded")
			logs := d.logs.String()
			assert.Equal(t, 1, strings.Count(logs, "session degraded"), "degraded is logged once")
			assert.Contains(t, logs, "level=ERROR")

			sent := 0
			for _, s := range streams {
				sent += len(s.SentAudio())
			}
			require.NoError(t, transcriptionTestEnd(t, sess))
			assert.ErrorIs(t, sess.HandleAudio(context.Background(), []byte{1}), domain.ErrSessionEnded)

			// Frames after degrading are dropped, not sent.
			total := 0
			for _, s := range streams {
				total += len(s.SentAudio())
			}
			assert.Equal(t, sent, total)
			assert.Less(t, total, 6+2, "audio stops being sent once degraded")
		})
	}
}

func TestTranscriptionSession_End(t *testing.T) {
	t.Run("idempotent", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		var mu sync.Mutex
		finishes := 0
		stream.FinishFn = func(context.Context) error {
			mu.Lock()
			finishes++
			mu.Unlock()
			stream.CloseResults()
			return nil
		}
		d := transcriptionTestNew(stream)
		sess := d.transcriptionTestStart(t)

		require.NoError(t, transcriptionTestEnd(t, sess))
		require.NoError(t, transcriptionTestEnd(t, sess))
		endStream, err := sess.HandleDTMF(context.Background(), "#")
		require.NoError(t, err)
		assert.True(t, endStream)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, finishes)
	})

	t.Run("10 concurrent calls all wait for the same completion", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		finishStarted := make(chan struct{})
		release := make(chan struct{})
		var mu sync.Mutex
		finishes := 0
		stream.FinishFn = func(context.Context) error {
			mu.Lock()
			finishes++
			mu.Unlock()
			close(finishStarted)
			<-release
			stream.Emit(transcriptionTestSeg("last words", 0))
			stream.CloseResults()
			return nil
		}
		sess := d.transcriptionTestStart(t)

		const callers = 10
		errs := make(chan error, callers)
		for i := 0; i < callers; i++ {
			go func() { errs <- transcriptionTestEnd(t, sess) }()
		}
		<-finishStarted

		// A caller whose own ctx is already done stops waiting.
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		assert.ErrorIs(t, sess.End(cancelled), context.Canceled)

		select {
		case err := <-errs:
			t.Fatalf("End returned before the flush finished: %v", err)
		default:
		}

		close(release)
		for i := 0; i < callers; i++ {
			select {
			case err := <-errs:
				assert.NoError(t, err)
			case <-time.After(transcriptionTestWait):
				t.Fatal("End did not return")
			}
		}
		assert.Equal(t, []string{"last words"}, d.transcriptionTestSavedTexts())
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, finishes)
	})

	hangs := []struct {
		name   string
		finish func(stream *fakeTranscriptionStream, closed <-chan struct{}) func(context.Context) error
	}{
		{
			name: "Finish honours ctx",
			finish: func(*fakeTranscriptionStream, <-chan struct{}) func(context.Context) error {
				return func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				}
			},
		},
		{
			name: "Finish ignores ctx until Close",
			finish: func(_ *fakeTranscriptionStream, closed <-chan struct{}) func(context.Context) error {
				return func(context.Context) error {
					<-closed
					return errors.New("closed while finishing")
				}
			},
		},
	}
	for _, tt := range hangs {
		t.Run("respects ctx deadline when "+tt.name, func(t *testing.T) {
			stream := newFakeTranscriptionStream()
			closed := make(chan struct{})
			var closeOnce sync.Once
			stream.CloseFn = func() error {
				closeOnce.Do(func() { close(closed) })
				return nil
			}
			stream.FinishFn = tt.finish(stream, closed)
			d := transcriptionTestNew(stream)
			sess := d.transcriptionTestStart(t)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := sess.End(ctx)

			assert.ErrorIs(t, err, context.DeadlineExceeded)
			select {
			case <-closed:
			default:
				t.Fatal("End must close the stream after timing out")
			}
			assert.True(t, transcriptionTestResultsClosed(stream))
			assert.ErrorIs(t, sess.End(context.Background()), context.DeadlineExceeded,
				"later calls report the first End's outcome")
			assert.ErrorIs(t, sess.HandleAudio(context.Background(), []byte{1}), domain.ErrSessionEnded)
			assert.Contains(t, d.logs.String(), "session end timed out")
		})
	}

	t.Run("logs a Finish error and still saves the flushed lines", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		stream.FinishFn = func(context.Context) error {
			stream.Emit(transcriptionTestSeg("flushed", 0))
			stream.CloseResults()
			return errors.New("close frame rejected")
		}
		stream.CloseFn = func() error { return errors.New("already closed") }
		d := transcriptionTestNew(stream)
		sess := d.transcriptionTestStart(t)

		require.NoError(t, transcriptionTestEnd(t, sess))
		assert.Equal(t, []string{"flushed"}, d.transcriptionTestSavedTexts())
		assert.Contains(t, d.logs.String(), "close frame rejected")
	})
}

func TestTranscriptionSession_HandleAudio(t *testing.T) {
	t.Run("forwards frames", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		sess := d.transcriptionTestStart(t)

		require.NoError(t, sess.HandleAudio(context.Background(), []byte{1, 2}))
		require.NoError(t, sess.HandleAudio(context.Background(), []byte{3}))

		assert.Equal(t, [][]byte{{1, 2}, {3}}, stream.SentAudio())
		assert.Equal(t, 0, d.transcriptionTestAppendCalls(), "no database work on the audio path")
	})

	t.Run("after End returns ErrSessionEnded", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		d := transcriptionTestNew(stream)
		sess := d.transcriptionTestStart(t)
		require.NoError(t, transcriptionTestEnd(t, sess))

		err := sess.HandleAudio(context.Background(), []byte{1})

		assert.ErrorIs(t, err, domain.ErrSessionEnded)
		assert.Empty(t, stream.SentAudio())
		assert.Equal(t, 1, d.transcriptionTestOpenCalls(), "no reopen after End")
	})

	t.Run("send failing because End ran concurrently does not reopen", func(t *testing.T) {
		stream := newFakeTranscriptionStream()
		inSend := make(chan struct{})
		release := make(chan struct{})
		stream.SendAudioFn = func([]byte) error {
			close(inSend)
			<-release
			return domain.ErrStreamClosed
		}
		d := transcriptionTestNew(stream, newFakeTranscriptionStream())
		sess := d.transcriptionTestStart(t)

		result := make(chan error, 1)
		go func() { result <- sess.HandleAudio(context.Background(), []byte{1}) }()
		<-inSend
		require.NoError(t, transcriptionTestEnd(t, sess))
		close(release)

		assert.ErrorIs(t, <-result, domain.ErrSessionEnded)
		assert.Equal(t, 1, d.transcriptionTestOpenCalls())
	})

	t.Run("End during reopen closes the new stream", func(t *testing.T) {
		first := newFakeTranscriptionStream()
		first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
		second := newFakeTranscriptionStream()
		d := transcriptionTestNew(first, second)
		sess := d.transcriptionTestStart(t)

		inOpen := make(chan struct{})
		release := make(chan struct{})
		open := d.stt.OpenFn
		d.stt.OpenFn = func(ctx context.Context) (port.TranscriptionStream, error) {
			close(inOpen)
			<-release
			return open(ctx)
		}

		result := make(chan error, 1)
		go func() { result <- sess.HandleAudio(context.Background(), []byte{1}) }()
		<-inOpen
		require.NoError(t, transcriptionTestEnd(t, sess))
		close(release)

		assert.ErrorIs(t, <-result, domain.ErrSessionEnded)
		assert.True(t, transcriptionTestResultsClosed(second), "the late stream must not leak")
		assert.Empty(t, second.SentAudio())
	})

	t.Run("resend failing because End ran concurrently reports ErrSessionEnded", func(t *testing.T) {
		first := newFakeTranscriptionStream()
		first.SendAudioFn = func([]byte) error { return transcriptionTestErrSend }
		second := newFakeTranscriptionStream()
		inSend := make(chan struct{})
		release := make(chan struct{})
		second.SendAudioFn = func([]byte) error {
			close(inSend)
			<-release
			return domain.ErrStreamClosed
		}
		d := transcriptionTestNew(first, second)
		sess := d.transcriptionTestStart(t)

		result := make(chan error, 1)
		go func() { result <- sess.HandleAudio(context.Background(), []byte{1}) }()
		<-inSend
		require.NoError(t, transcriptionTestEnd(t, sess))
		close(release)

		assert.ErrorIs(t, <-result, domain.ErrSessionEnded)
		assert.NotContains(t, d.logs.String(), "session degraded")
	})
}
