// Package deepgram implements port.SpeechToText and port.TranscriptionStream
// on top of Deepgram's live streaming transcription WebSocket API. See
// IMPLEMENTATION.md §6.2 for the protocol reference.
package deepgram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// defaultURL is the Deepgram live streaming endpoint.
const defaultURL = "wss://api.deepgram.com/v1/listen"

// handshakeTimeout bounds how long Open waits for the WebSocket handshake.
const handshakeTimeout = 10 * time.Second

// Config configures a Client.
type Config struct {
	// APIKey authenticates with Deepgram. Never logged.
	APIKey string
	// Model selects the Deepgram speech model, e.g. "nova-2".
	Model string
	// URL is the Deepgram live streaming endpoint. Defaults to defaultURL
	// when empty. Overridable for tests.
	URL string
}

// Option customizes a Client constructed by New.
type Option func(*Client)

// WithClock overrides the clock used to stamp when a stream was opened.
// Tests use this for deterministic SpokenAt values.
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		c.now = now
	}
}

// Client implements port.SpeechToText, opening streaming transcription
// sessions against Deepgram.
type Client struct {
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

var _ port.SpeechToText = (*Client)(nil)

// New constructs a Client. logger may be nil, in which case logs are
// discarded.
func New(cfg Config, logger *slog.Logger, opts ...Option) *Client {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	c := &Client{
		cfg:    cfg,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Open dials Deepgram and starts a new streaming transcription session.
func (c *Client) Open(ctx context.Context) (port.TranscriptionStream, error) {
	target, err := buildURL(c.cfg)
	if err != nil {
		return nil, fmt.Errorf("deepgram: build url: %w", err)
	}

	header := http.Header{}
	header.Set("Authorization", "Token "+c.cfg.APIKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
	}

	conn, resp, err := dialer.DialContext(ctx, target.String(), header)
	if err != nil {
		status := "unknown"
		if resp != nil {
			status = resp.Status
		}
		return nil, fmt.Errorf("deepgram: open stream failed (status %s): %w", status, err)
	}

	openedAt := c.now()

	return newStream(conn, c.logger, openedAt), nil
}

// buildURL applies the query parameters from IMPLEMENTATION.md §6.2 to the
// configured (or default) Deepgram endpoint.
func buildURL(cfg Config) (*url.URL, error) {
	base := cfg.URL
	if base == "" {
		base = defaultURL
	}

	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("model", cfg.Model)
	q.Set("encoding", "mulaw")
	q.Set("sample_rate", "8000")
	q.Set("channels", "1")
	q.Set("punctuate", "true")
	q.Set("smart_format", "true")
	q.Set("interim_results", "false")
	u.RawQuery = q.Encode()

	return u, nil
}

// discardWriter is an io.Writer that discards everything written to it, used
// as the default log sink when no logger is supplied.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
