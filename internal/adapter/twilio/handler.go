// Package twilio is the Twilio-facing inbound adapter: the voice webhooks that
// answer with TwiML, the Media Streams WebSocket that feeds audio and keypresses
// into the core, request signature validation, and signed stream parameters.
// It translates between Twilio's protocol and the core's driving ports and
// contains no business rules. See IMPLEMENTATION.md §6.1.
package twilio

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/twilio/twilio-go/client"

	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// Route paths, relative to Config.PublicBaseURL.
const (
	pathVoice         = "/twilio/voice"
	pathIncident      = "/twilio/voice/incident"
	pathConfirmPrompt = "/twilio/voice/confirm-prompt"
	pathConfirm       = "/twilio/voice/confirm"
	pathResume        = "/twilio/voice/resume"
	pathMedia         = "/twilio/media"
)

// Media stream limits (IMPLEMENTATION.md WP8).
const (
	mediaReadLimit        = 64 << 10
	mediaReadTimeout      = 30 * time.Second
	mediaCloseTimeout     = time.Second
	mediaStartTimeout     = 10 * time.Second
	mediaDTMFTimeout      = 5 * time.Second
	mediaEndTimeout       = 5 * time.Second
	webhookMaxBodyBytes   = 64 << 10
	twilioSignatureHeader = "X-Twilio-Signature"
)

// Config configures a Handler.
type Config struct {
	// AccountSID must match the AccountSid Twilio sends with every webhook.
	AccountSID string
	// AuthToken signs X-Twilio-Signature. Never logged.
	AuthToken string
	// PublicBaseURL is the public https:// URL Twilio calls (the ngrok URL),
	// with no trailing slash. Signature URLs are built from it, never from Host.
	PublicBaseURL string
	// StreamSigningSecret signs the stream token. Never logged.
	StreamSigningSecret string
}

// Handler serves the Twilio voice webhooks and the Media Streams WebSocket.
type Handler struct {
	cfg           Config
	calls         port.CallService
	transcription port.TranscriptionService
	logger        *slog.Logger

	validator client.RequestValidator
	// mediaURL is the wss:// URL of the media endpoint. It is both the
	// <Stream url> and the URL Twilio signs on the WebSocket upgrade.
	mediaURL string
	upgrader websocket.Upgrader
	// readTimeout is the per-message read deadline on the media WebSocket.
	readTimeout time.Duration
}

// NewHandler returns a Handler. A nil logger discards log output.
func NewHandler(cfg Config, calls port.CallService, transcription port.TranscriptionService, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")

	host := ""
	if u, err := url.Parse(cfg.PublicBaseURL); err == nil {
		host = u.Host
	} else {
		logger.Error("twilio: invalid public base URL", "error", err)
	}

	return &Handler{
		cfg:           cfg,
		calls:         calls,
		transcription: transcription,
		logger:        logger,
		validator:     client.NewRequestValidator(cfg.AuthToken),
		mediaURL:      (&url.URL{Scheme: "wss", Host: host, Path: pathMedia}).String(),
		upgrader: websocket.Upgrader{
			// Twilio sends no Origin header; the signature is the authentication.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		readTimeout: mediaReadTimeout,
	}
}

// Register mounts the Twilio routes on r: the webhooks as POST behind the
// signature middleware, and the media WebSocket as GET with its upgrade-time
// signature check.
func (h *Handler) Register(r *mux.Router) {
	r.Handle(pathVoice, h.requireWebhookSignature(http.HandlerFunc(h.handleVoice))).Methods(http.MethodPost)
	r.Handle(pathIncident, h.requireWebhookSignature(http.HandlerFunc(h.handleIncident))).Methods(http.MethodPost)
	r.Handle(pathConfirmPrompt, h.requireWebhookSignature(http.HandlerFunc(h.handleConfirmPrompt))).Methods(http.MethodPost)
	r.Handle(pathConfirm, h.requireWebhookSignature(http.HandlerFunc(h.handleConfirm))).Methods(http.MethodPost)
	r.Handle(pathResume, h.requireWebhookSignature(http.HandlerFunc(h.handleResume))).Methods(http.MethodPost)
	r.HandleFunc(pathMedia, h.handleMedia).Methods(http.MethodGet)
}
