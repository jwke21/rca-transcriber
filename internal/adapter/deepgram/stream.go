package deepgram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// resultsBufferSize is the buffer depth of Results(). When full, the reader
// goroutine blocks, which provides backpressure.
const resultsBufferSize = 64

// Deepgram control messages. See IMPLEMENTATION.md §6.2.
var (
	finalizeMessage    = []byte(`{"type":"Finalize"}`)
	closeStreamMessage = []byte(`{"type":"CloseStream"}`)
)

// resultsMessage is the subset of Deepgram's "Results" message this adapter
// cares about.
type resultsMessage struct {
	Type     string  `json:"type"`
	IsFinal  bool    `json:"is_final"`
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Channel  struct {
		Alternatives []struct {
			Transcript string `json:"transcript"`
		} `json:"alternatives"`
	} `json:"channel"`
}

// Stream implements port.TranscriptionStream for one Deepgram connection.
type Stream struct {
	conn     *websocket.Conn
	logger   *slog.Logger
	openedAt time.Time

	results    chan domain.TranscriptSegment
	readerDone chan struct{}

	mu     sync.Mutex // guards writes to conn and closed
	closed bool

	closeOnce sync.Once
}

var _ port.TranscriptionStream = (*Stream)(nil)

// newStream constructs a Stream and starts its reader goroutine.
func newStream(conn *websocket.Conn, logger *slog.Logger, openedAt time.Time) *Stream {
	s := &Stream{
		conn:       conn,
		logger:     logger,
		openedAt:   openedAt,
		results:    make(chan domain.TranscriptSegment, resultsBufferSize),
		readerDone: make(chan struct{}),
	}
	go s.readLoop()
	return s
}

// readLoop reads frames from Deepgram until the connection closes or errors,
// emitting final transcript segments to results. It owns closing both
// results and readerDone, each exactly once.
func (s *Stream) readLoop() {
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.results)
		close(s.readerDone)
	}()

	for {
		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			s.logger.Debug("deepgram: reader stopping", "err", err)
			return
		}

		if msgType != websocket.TextMessage {
			continue
		}

		var msg resultsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.logger.Warn("deepgram: malformed message from server", "err", err)
			continue
		}

		if msg.Type != "Results" || !msg.IsFinal {
			continue
		}
		if len(msg.Channel.Alternatives) == 0 {
			continue
		}

		text := msg.Channel.Alternatives[0].Transcript
		if strings.TrimSpace(text) == "" {
			continue
		}

		segment := domain.TranscriptSegment{
			Text:     text,
			SpokenAt: s.openedAt.Add(time.Duration(msg.Start * float64(time.Second))),
			Duration: time.Duration(msg.Duration * float64(time.Second)),
			IsFinal:  true,
		}

		s.results <- segment
	}
}

// SendAudio forwards raw 8 kHz mu-law audio as a binary frame.
func (s *Stream) SendAudio(mulaw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return domain.ErrStreamClosed
	}

	if err := s.conn.WriteMessage(websocket.BinaryMessage, mulaw); err != nil {
		s.closed = true
		return fmt.Errorf("deepgram: send audio: %w", err)
	}
	return nil
}

// Results delivers final transcript segments. It is closed when the stream
// ends for any reason.
func (s *Stream) Results() <-chan domain.TranscriptSegment {
	return s.results
}

// Finish asks Deepgram to flush buffered audio and close the connection,
// then waits for the reader goroutine to exit or for ctx to finish.
func (s *Stream) Finish(ctx context.Context) error {
	_ = s.writeControl(finalizeMessage)
	_ = s.writeControl(closeStreamMessage)

	select {
	case <-s.readerDone:
		return nil
	case <-ctx.Done():
		_ = s.Close()
		return ctx.Err()
	}
}

// writeControl sends a control text frame, sharing the write mutex with
// SendAudio. It returns domain.ErrStreamClosed if the stream is already
// closed.
func (s *Stream) writeControl(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return domain.ErrStreamClosed
	}

	if err := s.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		s.closed = true
		return fmt.Errorf("deepgram: write control message: %w", err)
	}
	return nil
}

// Close aborts the stream immediately. Safe to call more than once.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	var err error
	s.closeOnce.Do(func() {
		err = s.conn.Close()
	})
	return err
}
