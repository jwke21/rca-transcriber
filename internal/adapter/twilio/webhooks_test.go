package twilio

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

var errBoom = errors.New("database is down")

func testEngineer() domain.Engineer {
	return domain.Engineer{ID: testEngineerID, PhoneNumber: testCallerPhone}
}

func recordingStart(resumed bool) port.RecordingStart {
	return port.RecordingStart{
		Engineer: testEngineer(),
		Incident: domain.Incident{ID: testIncidentID, Status: domain.IncidentStatusOpen},
		Resumed:  resumed,
	}
}

func serve(t *testing.T, calls *fakeCalls, requestURI string, extra map[string]string) xmlNode {
	t.Helper()
	_, router := newTestRouter(calls, newFakeTranscription())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, signedRequest(requestURI, webhookParams(extra)))
	return parseTwiML(t, rec)
}

// assertSayHangup checks a <Say>text</Say><Hangup/> response.
func assertSayHangup(t *testing.T, root xmlNode, text string) {
	t.Helper()
	require.Equal(t, []string{"Say", "Hangup"}, root.names())
	assert.Equal(t, text, root.Children[0].Text)
}

// assertStreamTwiML checks the §6.1 stream TwiML for the given start.
func assertStreamTwiML(t *testing.T, root xmlNode, wantSay string, engineerID, incidentID int64) {
	t.Helper()
	require.Equal(t, []string{"Say", "Connect", "Redirect"}, root.names())
	assert.Equal(t, wantSay, root.Children[0].Text)

	stream := root.Children[1].child(t, "Stream")
	streamURL, err := url.Parse(stream.attr("url"))
	require.NoError(t, err)
	assert.Equal(t, "wss", streamURL.Scheme)
	assert.Equal(t, "rca.example.ngrok.app", streamURL.Host)
	assert.Equal(t, "/twilio/media", streamURL.Path)
	assert.Empty(t, streamURL.RawQuery, "stream URL must not contain a query string")

	params := map[string]string{}
	for _, p := range stream.Children {
		require.Equal(t, "Parameter", p.XMLName.Local)
		params[p.attr("name")] = p.attr("value")
	}
	require.Len(t, params, 3)
	assert.Equal(t, fmt.Sprint(incidentID), params["incident_id"])
	assert.Equal(t, fmt.Sprint(engineerID), params["engineer_id"])
	assert.True(t, verifyStreamToken(testStreamSecret, incidentID, engineerID, testCallSID, params["token"]),
		"token must verify for the webhook's CallSid")
	assert.False(t, verifyStreamToken(testStreamSecret, incidentID, engineerID, "CAother", params["token"]))

	redirect := root.Children[2]
	assert.Equal(t, "POST", redirect.attr("method"))
	assert.Equal(t, fmt.Sprintf("%s/twilio/voice/confirm-prompt?incident_id=%d", testBaseURL, incidentID), redirect.Text)
}

func assertInvalidIncident(t *testing.T, root xmlNode) {
	t.Helper()
	require.Equal(t, []string{"Say", "Redirect"}, root.names())
	assert.Equal(t, "That incident number is not valid.", root.Children[0].Text)
	assert.Equal(t, testBaseURL+"/twilio/voice", root.Children[1].Text)
	assert.Equal(t, "POST", root.Children[1].attr("method"))
}

const (
	sayUnauthorized = "This phone number is not authorized."
	sayGenericError = "Something went wrong. Please try again."
	sayBadParam     = "Something went wrong. Goodbye."
	sayBusy         = "An R C A is already being generated for incident 4 8 2 1. Try again later."
	sayNewStream    = "New incident 4 8 2 1. Start narrating. Press pound when the incident is resolved."
	sayResumeStream = "Resuming incident 4 8 2 1. Start narrating. Press pound when the incident is resolved."
)

func TestHandleVoice(t *testing.T) {
	t.Run("authorized caller gets the incident Gather", func(t *testing.T) {
		calls := &fakeCalls{engineer: testEngineer()}
		root := serve(t, calls, "/twilio/voice", nil)

		assert.Equal(t, []string{"AuthorizeCaller"}, calls.Calls())
		assert.Equal(t, testCallerPhone, calls.gotPhone)
		require.Equal(t, []string{"Gather", "Say", "Hangup"}, root.names())
		gather := root.Children[0]
		assert.Equal(t, "dtmf", gather.attr("input"))
		assert.Equal(t, testBaseURL+"/twilio/voice/incident", gather.attr("action"))
		assert.Equal(t, "POST", gather.attr("method"))
		assert.Equal(t, "#", gather.attr("finishOnKey"))
		assert.Equal(t, "10", gather.attr("timeout"))
		assert.Empty(t, gather.attr("numDigits"))
		assert.Equal(t, "Enter the incident number, then press pound.", gather.child(t, "Say").Text)
		assert.Equal(t, "No incident number received. Goodbye.", root.Children[1].Text)
	})

	tests := []struct {
		name    string
		err     error
		wantSay string
	}{
		{"unauthorized caller", domain.ErrUnauthorizedCaller, sayUnauthorized},
		{"generic error", errBoom, sayGenericError},
		{"not found is generic", domain.ErrNotFound, sayGenericError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := serve(t, &fakeCalls{authorizeErr: tt.err}, "/twilio/voice", nil)
			assertSayHangup(t, root, tt.wantSay)
		})
	}
}

func TestHandleIncident(t *testing.T) {
	tests := []struct {
		name   string
		calls  *fakeCalls
		digits string
		check  func(t *testing.T, root xmlNode)
	}{
		{
			name:   "new incident",
			calls:  &fakeCalls{begin: recordingStart(false)},
			digits: testIncidentDigit,
			check: func(t *testing.T, root xmlNode) {
				assertStreamTwiML(t, root, sayNewStream, testEngineerID, testIncidentID)
			},
		},
		{
			name:   "resumed incident",
			calls:  &fakeCalls{begin: recordingStart(true)},
			digits: testIncidentDigit,
			check: func(t *testing.T, root xmlNode) {
				assertStreamTwiML(t, root, sayResumeStream, testEngineerID, testIncidentID)
			},
		},
		{
			name:   "unauthorized",
			calls:  &fakeCalls{beginErr: domain.ErrUnauthorizedCaller},
			digits: testIncidentDigit,
			check:  func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayUnauthorized) },
		},
		{
			name:   "invalid digits",
			calls:  &fakeCalls{beginErr: domain.ErrInvalidIncidentNumber},
			digits: "12a",
			check:  assertInvalidIncident,
		},
		{
			name:   "busy incident speaks the entered number",
			calls:  &fakeCalls{beginErr: fmt.Errorf("wrapped: %w", domain.ErrIncidentBusy)},
			digits: testIncidentDigit,
			check:  func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayBusy) },
		},
		{
			name:   "busy with unparseable digits falls back to generic",
			calls:  &fakeCalls{beginErr: domain.ErrIncidentBusy},
			digits: "",
			check:  func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
		{
			name:   "invalid transition is generic",
			calls:  &fakeCalls{beginErr: domain.ErrInvalidTransition},
			digits: testIncidentDigit,
			check:  func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
		{
			name:   "generic error",
			calls:  &fakeCalls{beginErr: errBoom},
			digits: testIncidentDigit,
			check:  func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := serve(t, tt.calls, "/twilio/voice/incident", map[string]string{"Digits": tt.digits})
			assert.Equal(t, []string{"BeginRecording"}, tt.calls.Calls())
			assert.Equal(t, testCallerPhone, tt.calls.gotPhone)
			assert.Equal(t, tt.digits, tt.calls.gotDigits)
			tt.check(t, root)
		})
	}
}

func TestHandleConfirmPrompt(t *testing.T) {
	t.Run("gather attributes", func(t *testing.T) {
		calls := &fakeCalls{engineer: testEngineer()}
		root := serve(t, calls, "/twilio/voice/confirm-prompt?incident_id=4821", nil)

		assert.Equal(t, []string{"AuthorizeCaller"}, calls.Calls())
		require.Equal(t, []string{"Gather", "Redirect"}, root.names())
		gather := root.Children[0]
		assert.Equal(t, "dtmf", gather.attr("input"))
		assert.Equal(t, "1", gather.attr("numDigits"))
		assert.Equal(t, testBaseURL+"/twilio/voice/confirm?incident_id=4821", gather.attr("action"))
		assert.Equal(t, "POST", gather.attr("method"))
		assert.Equal(t, "10", gather.attr("timeout"))
		assert.Empty(t, gather.attr("finishOnKey"))
		assert.Equal(t,
			"Press 1 to confirm the incident is resolved and generate the R C A. Press any other key to keep recording.",
			gather.child(t, "Say").Text)
		assert.Equal(t, testBaseURL+"/twilio/voice/resume?incident_id=4821", root.Children[1].Text)
		assert.Equal(t, "POST", root.Children[1].attr("method"))
	})

	tests := []struct {
		name    string
		err     error
		wantSay string
	}{
		{"unauthorized", domain.ErrUnauthorizedCaller, sayUnauthorized},
		{"generic error", errBoom, sayGenericError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := serve(t, &fakeCalls{authorizeErr: tt.err}, "/twilio/voice/confirm-prompt?incident_id=4821", nil)
			assertSayHangup(t, root, tt.wantSay)
		})
	}
}

func TestHandleConfirm(t *testing.T) {
	tests := []struct {
		name      string
		calls     *fakeCalls
		digits    string
		wantCalls []string
		check     func(t *testing.T, root xmlNode)
	}{
		{
			name:      "confirmed says goodbye and hangs up",
			calls:     &fakeCalls{confirmed: true},
			digits:    "1",
			wantCalls: []string{"ConfirmResolution"},
			check: func(t *testing.T, root xmlNode) {
				assertSayHangup(t, root, "Generating the R C A for incident 4 8 2 1. A pull request will be opened shortly. Goodbye.")
			},
		},
		{
			name:      "not confirmed resumes the stream",
			calls:     &fakeCalls{confirmed: false, resume: recordingStart(true)},
			digits:    "2",
			wantCalls: []string{"ConfirmResolution", "ResumeRecording"},
			check: func(t *testing.T, root xmlNode) {
				assertStreamTwiML(t, root, sayResumeStream, testEngineerID, testIncidentID)
			},
		},
		{
			name:      "confirm error unauthorized",
			calls:     &fakeCalls{confirmErr: domain.ErrUnauthorizedCaller},
			digits:    "1",
			wantCalls: []string{"ConfirmResolution"},
			check:     func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayUnauthorized) },
		},
		{
			name:      "confirm invalid transition is generic",
			calls:     &fakeCalls{confirmErr: fmt.Errorf("x: %w", domain.ErrInvalidTransition)},
			digits:    "1",
			wantCalls: []string{"ConfirmResolution"},
			check:     func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
		{
			name:      "confirm not found is generic",
			calls:     &fakeCalls{confirmErr: domain.ErrNotFound},
			digits:    "1",
			wantCalls: []string{"ConfirmResolution"},
			check:     func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
		{
			name:      "not confirmed, resume busy",
			calls:     &fakeCalls{resumeErr: domain.ErrIncidentBusy},
			digits:    "9",
			wantCalls: []string{"ConfirmResolution", "ResumeRecording"},
			check:     func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayBusy) },
		},
		{
			name:      "not confirmed, resume generic error",
			calls:     &fakeCalls{resumeErr: errBoom},
			digits:    "",
			wantCalls: []string{"ConfirmResolution", "ResumeRecording"},
			check:     func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := serve(t, tt.calls, "/twilio/voice/confirm?incident_id=4821", map[string]string{"Digits": tt.digits})
			assert.Equal(t, tt.wantCalls, tt.calls.Calls())
			assert.Equal(t, testIncidentID, tt.calls.gotIncidentID)
			assert.Equal(t, testCallerPhone, tt.calls.gotPhone)
			tt.check(t, root)
		})
	}
}

func TestHandleResume(t *testing.T) {
	tests := []struct {
		name  string
		calls *fakeCalls
		check func(t *testing.T, root xmlNode)
	}{
		{
			name:  "resumes the stream",
			calls: &fakeCalls{resume: recordingStart(true)},
			check: func(t *testing.T, root xmlNode) {
				assertStreamTwiML(t, root, sayResumeStream, testEngineerID, testIncidentID)
			},
		},
		{
			name:  "unauthorized",
			calls: &fakeCalls{resumeErr: domain.ErrUnauthorizedCaller},
			check: func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayUnauthorized) },
		},
		{
			name:  "busy",
			calls: &fakeCalls{resumeErr: domain.ErrIncidentBusy},
			check: func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayBusy) },
		},
		{
			name:  "not found is generic",
			calls: &fakeCalls{resumeErr: fmt.Errorf("call: get incident: %w", domain.ErrNotFound)},
			check: func(t *testing.T, root xmlNode) { assertSayHangup(t, root, sayGenericError) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := serve(t, tt.calls, "/twilio/voice/resume?incident_id=4821", nil)
			assert.Equal(t, []string{"ResumeRecording"}, tt.calls.Calls())
			assert.Equal(t, testIncidentID, tt.calls.gotIncidentID)
			tt.check(t, root)
		})
	}
}

func TestIncidentIDQueryParam(t *testing.T) {
	paths := []string{"/twilio/voice/confirm-prompt", "/twilio/voice/confirm", "/twilio/voice/resume"}
	queries := map[string]string{
		"missing":     "",
		"non-numeric": "?incident_id=abc",
		"zero":        "?incident_id=0",
		"negative":    "?incident_id=-5",
		"empty":       "?incident_id=",
	}
	for _, path := range paths {
		for name, query := range queries {
			t.Run(path+" "+name, func(t *testing.T) {
				calls := &fakeCalls{confirmed: true, resume: recordingStart(true)}
				root := serve(t, calls, path+query, map[string]string{"Digits": "1"})
				assertSayHangup(t, root, sayBadParam)
				assert.Empty(t, calls.Calls(), "core must not be called")
			})
		}
	}
}

func TestTwiMLIsEscaped(t *testing.T) {
	// A base URL containing XML metacharacters must still produce valid XML.
	cfg := testConfig()
	cfg.PublicBaseURL = "https://a.example/x?y=1&z=<2>"
	h := NewHandler(cfg, &fakeCalls{}, newFakeTranscription(), nil)
	rec := httptest.NewRecorder()
	h.write(rec, httptest.NewRequest("POST", "/", nil), confirmPromptTwiML(h.cfg.PublicBaseURL, 1))
	root := parseTwiML(t, rec)
	assert.Equal(t, "https://a.example/x?y=1&z=<2>/twilio/voice/confirm?incident_id=1", root.Children[0].attr("action"))
}
