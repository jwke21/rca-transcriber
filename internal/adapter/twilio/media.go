package twilio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// mediaMessage is a Twilio Media Streams message (Twilio to server). Only the
// fields this adapter uses are decoded.
type mediaMessage struct {
	Event string `json:"event"`
	Start *struct {
		CallSid          string            `json:"callSid"`
		StreamSid        string            `json:"streamSid"`
		CustomParameters map[string]string `json:"customParameters"`
	} `json:"start"`
	Media *struct {
		Track   string `json:"track"`
		Payload string `json:"payload"`
	} `json:"media"`
	DTMF *struct {
		Digit string `json:"digit"`
	} `json:"dtmf"`
}

// handleMedia serves the Media Streams WebSocket. The signature is checked
// before upgrading; an invalid one gets 403 and no upgrade.
func (h *Handler) handleMedia(w http.ResponseWriter, r *http.Request) {
	if !h.validUpgradeSignature(r) {
		h.logger.WarnContext(r.Context(), "twilio: media upgrade rejected: bad signature")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader has already written the HTTP error.
		h.logger.WarnContext(r.Context(), "twilio: media upgrade failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(mediaReadLimit)

	s := &mediaStream{h: h, conn: conn, reqCtx: r.Context(), logger: h.logger}
	s.run()
}

// mediaStream is the state of one media WebSocket. All of its methods run on
// the handler goroutine, which is the connection's only reader and only writer.
type mediaStream struct {
	h      *Handler
	conn   *websocket.Conn
	reqCtx context.Context
	logger *slog.Logger

	session port.MediaSession
	ended   bool
}

// run reads messages until the stream should close. Whatever the exit path
// (stop, "#", client close, read error, read deadline, bad token), the
// deferred endSession ends a started session.
func (s *mediaStream) run() {
	defer s.endSession()
	for {
		if err := s.conn.SetReadDeadline(time.Now().Add(s.h.readTimeout)); err != nil {
			s.logger.WarnContext(s.reqCtx, "twilio: media set read deadline", "error", err)
			return
		}
		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.logger.DebugContext(s.reqCtx, "twilio: media stream closed by client")
			} else {
				s.logger.InfoContext(s.reqCtx, "twilio: media stream read ended", "error", err)
			}
			return
		}
		if msgType != websocket.TextMessage {
			s.logger.DebugContext(s.reqCtx, "twilio: media ignoring non-text frame", "type", msgType)
			continue
		}
		if !s.handle(data) {
			return
		}
	}
}

// handle processes one message and reports whether to keep reading.
func (s *mediaStream) handle(data []byte) bool {
	var msg mediaMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		s.logger.WarnContext(s.reqCtx, "twilio: media skipping malformed message", "error", err)
		return true
	}

	switch msg.Event {
	case "start":
		return s.handleStart(msg)
	case "media":
		s.handleMedia(msg)
		return true
	case "dtmf":
		return s.handleDTMF(msg)
	case "stop":
		s.logger.DebugContext(s.reqCtx, "twilio: media stream stopped")
		s.endSession()
		return false
	case "connected", "mark":
		return true
	default:
		s.logger.DebugContext(s.reqCtx, "twilio: media ignoring unknown event", "event", msg.Event)
		return true
	}
}

// handleStart verifies the stream token and starts the transcription session.
func (s *mediaStream) handleStart(msg mediaMessage) bool {
	if msg.Start == nil {
		s.logger.WarnContext(s.reqCtx, "twilio: media start without start block")
		s.closeWith(websocket.ClosePolicyViolation, "invalid start")
		return false
	}
	params := msg.Start.CustomParameters
	incidentID, errIncident := domain.ParseIncidentNumber(params[paramIncidentID])
	engineerID, errEngineer := strconv.ParseInt(params[paramEngineerID], 10, 64)
	if errIncident != nil || errEngineer != nil || engineerID <= 0 ||
		!verifyStreamToken(s.h.cfg.StreamSigningSecret, incidentID, engineerID, msg.Start.CallSid, params[paramToken]) {
		s.logger.WarnContext(s.reqCtx, "twilio: media start rejected: bad stream parameters or token",
			"call_sid", msg.Start.CallSid)
		s.closeWith(websocket.ClosePolicyViolation, "invalid stream token")
		return false
	}

	if s.session != nil {
		s.logger.WarnContext(s.reqCtx, "twilio: media ignoring duplicate start")
		return true
	}

	s.logger = s.logger.With("call_sid", msg.Start.CallSid, "stream_sid", msg.Start.StreamSid,
		"incident_id", incidentID, "engineer_id", engineerID)

	ctx, cancel := context.WithTimeout(s.reqCtx, mediaStartTimeout)
	defer cancel()
	session, err := s.h.transcription.StartSession(ctx, incidentID, engineerID)
	if err != nil {
		s.logger.ErrorContext(s.reqCtx, "twilio: media start session failed", "error", err)
		s.closeWith(websocket.CloseInternalServerErr, "session unavailable")
		return false
	}
	s.session = session
	s.logger.InfoContext(s.reqCtx, "twilio: media stream started")
	return true
}

// handleMedia forwards inbound audio to the session.
func (s *mediaStream) handleMedia(msg mediaMessage) {
	if s.session == nil {
		s.logger.DebugContext(s.reqCtx, "twilio: media ignoring audio before start")
		return
	}
	if msg.Media == nil || msg.Media.Track != "inbound" {
		return
	}
	audio, err := base64.StdEncoding.DecodeString(msg.Media.Payload)
	if err != nil {
		s.logger.WarnContext(s.reqCtx, "twilio: media skipping frame with bad base64", "error", err)
		return
	}
	if err := s.session.HandleAudio(s.reqCtx, audio); err != nil {
		level := slog.LevelWarn
		if errors.Is(err, domain.ErrSessionEnded) {
			level = slog.LevelDebug
		}
		s.logger.Log(s.reqCtx, level, "twilio: media handle audio", "error", err)
	}
}

// handleDTMF passes a keypress to the session. When the session asks to end
// the stream ("#"), it closes the WebSocket so Twilio moves on to the
// confirmation prompt.
func (s *mediaStream) handleDTMF(msg mediaMessage) bool {
	if s.session == nil || msg.DTMF == nil {
		s.logger.DebugContext(s.reqCtx, "twilio: media ignoring dtmf before start")
		return true
	}
	// The flush must survive a disconnect but must not hang the loop forever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.reqCtx), mediaDTMFTimeout)
	defer cancel()
	endStream, err := s.session.HandleDTMF(ctx, msg.DTMF.Digit)
	if err != nil {
		s.logger.WarnContext(s.reqCtx, "twilio: media handle dtmf", "error", err)
	}
	if !endStream {
		return true
	}
	s.closeWith(websocket.CloseNormalClosure, "")
	return false
}

// closeWith writes a close frame with a short deadline. The caller then
// returns, and handleMedia closes the connection.
func (s *mediaStream) closeWith(code int, text string) {
	deadline := time.Now().Add(mediaCloseTimeout)
	if err := s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), deadline); err != nil {
		s.logger.DebugContext(s.reqCtx, "twilio: media write close frame", "error", err)
	}
}

// endSession ends the session once, if one was started. The request context
// may already be cancelled by the disconnect, so End gets its own bounded ctx.
func (s *mediaStream) endSession() {
	if s.session == nil || s.ended {
		return
	}
	s.ended = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.reqCtx), mediaEndTimeout)
	defer cancel()
	if err := s.session.End(ctx); err != nil {
		s.logger.ErrorContext(s.reqCtx, "twilio: media end session", "error", err)
		return
	}
	s.logger.InfoContext(s.reqCtx, "twilio: media stream ended")
}
