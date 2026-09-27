package twilio

import (
	"errors"
	"net/http"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// Names of the stream <Parameter>s and the webhook query parameter.
const (
	paramIncidentID = "incident_id"
	paramEngineerID = "engineer_id"
	paramToken      = "token"
	queryIncidentID = "incident_id"
)

// Every handler below runs behind requireWebhookSignature, so r.PostForm is
// already parsed and authenticated. Values are read from the POST form only;
// incident_id comes from the (signed) query string.

// handleVoice answers the incoming call: authorize, then gather the incident number.
func (h *Handler) handleVoice(w http.ResponseWriter, r *http.Request) {
	from := r.PostForm.Get("From")
	if _, err := h.calls.AuthorizeCaller(r.Context(), from); err != nil {
		h.writeError(w, r, "voice", from, 0, err)
		return
	}
	h.write(w, r, gatherIncidentTwiML(h.cfg.PublicBaseURL))
}

// handleIncident starts or resumes recording for the entered incident number.
func (h *Handler) handleIncident(w http.ResponseWriter, r *http.Request) {
	from := r.PostForm.Get("From")
	digits := r.PostForm.Get("Digits")
	start, err := h.calls.BeginRecording(r.Context(), from, digits)
	if err != nil {
		// The only incident number available here is the keypad input. The
		// core validates it before it can report ErrIncidentBusy.
		id, _ := domain.ParseIncidentNumber(digits)
		h.writeError(w, r, "incident", from, id, err)
		return
	}
	h.writeStream(w, r, start)
}

// handleConfirmPrompt asks the SRE to confirm resolution after the stream closes.
func (h *Handler) handleConfirmPrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := h.incidentID(w, r)
	if !ok {
		return
	}
	from := r.PostForm.Get("From")
	if _, err := h.calls.AuthorizeCaller(r.Context(), from); err != nil {
		h.writeError(w, r, "confirm-prompt", from, id, err)
		return
	}
	h.write(w, r, confirmPromptTwiML(h.cfg.PublicBaseURL, id))
}

// handleConfirm resolves the incident on "1"; any other key resumes recording.
func (h *Handler) handleConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := h.incidentID(w, r)
	if !ok {
		return
	}
	from := r.PostForm.Get("From")
	confirmed, err := h.calls.ConfirmResolution(r.Context(), from, id, r.PostForm.Get("Digits"))
	if err != nil {
		h.writeError(w, r, "confirm", from, id, err)
		return
	}
	if confirmed {
		h.write(w, r, confirmedTwiML(id))
		return
	}
	// ConfirmResolution reports only a bool, but the stream TwiML needs the
	// engineer and incident, so fetch them through ResumeRecording.
	h.resume(w, r, "confirm", from, id)
}

// handleResume resumes recording when the confirmation Gather gets no input.
func (h *Handler) handleResume(w http.ResponseWriter, r *http.Request) {
	id, ok := h.incidentID(w, r)
	if !ok {
		return
	}
	h.resume(w, r, "resume", r.PostForm.Get("From"), id)
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request, op, from string, id int64) {
	start, err := h.calls.ResumeRecording(r.Context(), from, id)
	if err != nil {
		h.writeError(w, r, op, from, id, err)
		return
	}
	h.writeStream(w, r, start)
}

// incidentID parses the incident_id query parameter. On failure it writes the
// "Something went wrong. Goodbye." TwiML and returns false.
func (h *Handler) incidentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := domain.ParseIncidentNumber(r.URL.Query().Get(queryIncidentID))
	if err != nil {
		h.logger.WarnContext(r.Context(), "twilio: missing or invalid incident_id", "path", r.URL.Path)
		h.write(w, r, badIncidentParamTwiML())
		return 0, false
	}
	return id, true
}

// writeStream writes the stream TwiML with a token bound to this call.
func (h *Handler) writeStream(w http.ResponseWriter, r *http.Request, start port.RecordingStart) {
	token := signStreamToken(h.cfg.StreamSigningSecret, start.Incident.ID, start.Engineer.ID, r.PostForm.Get("CallSid"))
	h.write(w, r, streamTwiML(h.cfg.PublicBaseURL, h.mediaURL, start, token))
}

// writeError maps a core error to TwiML per the §6.1 error table.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, op, from string, incidentID int64, err error) {
	switch {
	case errors.Is(err, domain.ErrUnauthorizedCaller):
		h.write(w, r, unauthorizedTwiML())
	case errors.Is(err, domain.ErrInvalidIncidentNumber):
		h.write(w, r, invalidIncidentTwiML(h.cfg.PublicBaseURL))
	case errors.Is(err, domain.ErrIncidentBusy) && incidentID > 0:
		h.write(w, r, busyTwiML(incidentID))
	default:
		h.logger.ErrorContext(r.Context(), "twilio: webhook failed",
			"op", op, "phone", domain.MaskPhone(from), "incident_id", incidentID, "error", err)
		h.write(w, r, errorTwiML())
	}
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request, resp Response) {
	writeTwiML(w, r, h.logger, resp)
}
