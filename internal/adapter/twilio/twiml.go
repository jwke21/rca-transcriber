package twilio

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// TwiML verbs used by this adapter. They are marshalled with encoding/xml;
// never build TwiML by string concatenation.

// Response is the TwiML document root. Verbs holds *Say, *Gather, *Connect,
// *Redirect and *Hangup values in the order Twilio should run them.
type Response struct {
	XMLName xml.Name `xml:"Response"`
	Verbs   []any
}

// Say speaks Text to the caller.
type Say struct {
	XMLName xml.Name `xml:"Say"`
	Text    string   `xml:",chardata"`
}

// Gather collects keypad digits and POSTs them to Action.
type Gather struct {
	XMLName     xml.Name `xml:"Gather"`
	Input       string   `xml:"input,attr"`
	Action      string   `xml:"action,attr"`
	Method      string   `xml:"method,attr"`
	NumDigits   int      `xml:"numDigits,attr,omitempty"`
	FinishOnKey string   `xml:"finishOnKey,attr,omitempty"`
	Timeout     int      `xml:"timeout,attr"`
	Say         *Say
}

// Connect opens a bidirectional media stream and blocks until the server
// closes it.
type Connect struct {
	XMLName xml.Name `xml:"Connect"`
	Stream  Stream
}

// Stream is the Media Streams WebSocket target. URL must not contain a query
// string; data is passed through Parameters.
type Stream struct {
	XMLName    xml.Name `xml:"Stream"`
	URL        string   `xml:"url,attr"`
	Parameters []Parameter
}

// Parameter is a custom parameter delivered in the stream's start message.
type Parameter struct {
	XMLName xml.Name `xml:"Parameter"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:"value,attr"`
}

// Redirect transfers control to the TwiML at URL.
type Redirect struct {
	XMLName xml.Name `xml:"Redirect"`
	Method  string   `xml:"method,attr"`
	URL     string   `xml:",chardata"`
}

// Hangup ends the call.
type Hangup struct {
	XMLName xml.Name `xml:"Hangup"`
}

// gatherIncidentTwiML prompts for the incident number (/twilio/voice).
func gatherIncidentTwiML(baseURL string) Response {
	return Response{Verbs: []any{
		&Gather{
			Input:       "dtmf",
			Action:      baseURL + pathIncident,
			Method:      http.MethodPost,
			FinishOnKey: "#",
			Timeout:     10,
			Say:         &Say{Text: "Enter the incident number, then press pound."},
		},
		&Say{Text: "No incident number received. Goodbye."},
		&Hangup{},
	}}
}

// streamTwiML announces the incident, connects the media stream and, once the
// server closes the stream, redirects to the confirmation prompt.
func streamTwiML(baseURL, mediaURL string, start port.RecordingStart, token string) Response {
	prefix := "New incident"
	if start.Resumed {
		prefix = "Resuming incident"
	}
	id := start.Incident.ID
	return Response{Verbs: []any{
		&Say{Text: fmt.Sprintf("%s %s. Start narrating. Press pound when the incident is resolved.", prefix, speakDigits(id))},
		&Connect{Stream: Stream{
			URL: mediaURL,
			Parameters: []Parameter{
				{Name: paramIncidentID, Value: strconv.FormatInt(id, 10)},
				{Name: paramEngineerID, Value: strconv.FormatInt(start.Engineer.ID, 10)},
				{Name: paramToken, Value: token},
			},
		}},
		&Redirect{Method: http.MethodPost, URL: incidentURL(baseURL, pathConfirmPrompt, id)},
	}}
}

// confirmPromptTwiML asks the SRE to confirm resolution (/twilio/voice/confirm-prompt).
func confirmPromptTwiML(baseURL string, incidentID int64) Response {
	return Response{Verbs: []any{
		&Gather{
			Input:     "dtmf",
			NumDigits: 1,
			Action:    incidentURL(baseURL, pathConfirm, incidentID),
			Method:    http.MethodPost,
			Timeout:   10,
			Say:       &Say{Text: "Press 1 to confirm the incident is resolved and generate the R C A. Press any other key to keep recording."},
		},
		&Redirect{Method: http.MethodPost, URL: incidentURL(baseURL, pathResume, incidentID)},
	}}
}

// confirmedTwiML says goodbye after the SRE confirms resolution.
func confirmedTwiML(incidentID int64) Response {
	return sayHangup(fmt.Sprintf("Generating the R C A for incident %s. A pull request will be opened shortly. Goodbye.", speakDigits(incidentID)))
}

// unauthorizedTwiML answers domain.ErrUnauthorizedCaller.
func unauthorizedTwiML() Response {
	return sayHangup("This phone number is not authorized.")
}

// invalidIncidentTwiML answers domain.ErrInvalidIncidentNumber and asks again.
func invalidIncidentTwiML(baseURL string) Response {
	return Response{Verbs: []any{
		&Say{Text: "That incident number is not valid."},
		&Redirect{Method: http.MethodPost, URL: baseURL + pathVoice},
	}}
}

// busyTwiML answers domain.ErrIncidentBusy.
func busyTwiML(incidentID int64) Response {
	return sayHangup(fmt.Sprintf("An R C A is already being generated for incident %s. Try again later.", speakDigits(incidentID)))
}

// badIncidentParamTwiML answers a missing or invalid incident_id query parameter.
func badIncidentParamTwiML() Response {
	return sayHangup("Something went wrong. Goodbye.")
}

// errorTwiML answers any other error.
func errorTwiML() Response {
	return sayHangup("Something went wrong. Please try again.")
}

func sayHangup(text string) Response {
	return Response{Verbs: []any{&Say{Text: text}, &Hangup{}}}
}

// incidentURL builds {baseURL}{path}?incident_id={id}.
func incidentURL(baseURL, path string, incidentID int64) string {
	return fmt.Sprintf("%s%s?%s=%d", baseURL, path, queryIncidentID, incidentID)
}

// writeTwiML marshals resp and writes it with HTTP 200.
func writeTwiML(w http.ResponseWriter, r *http.Request, logger *slog.Logger, resp Response) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(resp); err != nil {
		logger.ErrorContext(r.Context(), "twilio: marshal TwiML", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		logger.DebugContext(r.Context(), "twilio: write TwiML", "error", err)
	}
}
