package twilio

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

func TestWebhookSignature(t *testing.T) {
	const uri = "/twilio/voice"
	const queryURI = "/twilio/voice/confirm-prompt?incident_id=4821"
	params := webhookParams(nil)

	tests := []struct {
		name     string
		req      func() *http.Request
		wantCode int
	}{
		{
			name:     "valid signature",
			req:      func() *http.Request { return signedRequest(uri, params) },
			wantCode: http.StatusOK,
		},
		{
			name:     "valid signature with signed query string",
			req:      func() *http.Request { return signedRequest(queryURI, params) },
			wantCode: http.StatusOK,
		},
		{
			name:     "missing signature",
			req:      func() *http.Request { return rawRequest(uri, params, "") },
			wantCode: http.StatusForbidden,
		},
		{
			name:     "wrong signature",
			req:      func() *http.Request { return rawRequest(uri, params, "bm90LWEtc2lnbmF0dXJl") },
			wantCode: http.StatusForbidden,
		},
		{
			name: "wrong auth token",
			req: func() *http.Request {
				return rawRequest(uri, params, twilioSignature("other-token", testBaseURL+uri, params))
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "signed for the Host header URL instead of PublicBaseURL",
			req: func() *http.Request {
				return rawRequest(uri, params, twilioSignature(testAuthToken, "http://example.com"+uri, params))
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "signed for another path",
			req: func() *http.Request {
				return rawRequest(uri, params, twilioSignature(testAuthToken, testBaseURL+"/twilio/voice/incident", params))
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "query params are not signed as form params",
			req: func() *http.Request {
				withQuery := webhookParams(map[string]string{"incident_id": "4821"})
				return rawRequest(queryURI, params, twilioSignature(testAuthToken, testBaseURL+"/twilio/voice/confirm-prompt", withQuery))
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "tampered form",
			req: func() *http.Request {
				sig := twilioSignature(testAuthToken, testBaseURL+uri, params)
				return rawRequest(uri, webhookParams(map[string]string{"From": "+15555559999"}), sig)
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "account SID mismatch",
			req: func() *http.Request {
				return signedRequest(uri, webhookParams(map[string]string{"AccountSid": "AC99999999999999999999999999999999"}))
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "missing account SID",
			req: func() *http.Request {
				p := webhookParams(nil)
				delete(p, "AccountSid")
				return signedRequest(uri, p)
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "oversized body",
			req: func() *http.Request {
				return signedRequest(uri, webhookParams(map[string]string{"Junk": strings.Repeat("x", webhookMaxBodyBytes)}))
			},
			wantCode: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &fakeCalls{engineer: domain.Engineer{ID: testEngineerID, PhoneNumber: testCallerPhone}}
			_, router := newTestRouter(calls, newFakeTranscription())

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, tt.req())

			assert.Equal(t, tt.wantCode, rec.Code)
			if tt.wantCode == http.StatusForbidden {
				assert.Empty(t, rec.Body.String())
				assert.Empty(t, calls.Calls(), "core must not be called")
			} else {
				assert.Equal(t, []string{"AuthorizeCaller"}, calls.Calls())
			}
		})
	}
}

func TestWebhookRoutesArePostOnly(t *testing.T) {
	calls := &fakeCalls{}
	_, router := newTestRouter(calls, newFakeTranscription())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/twilio/voice", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, calls.Calls())
}

func TestMediaUpgradeSignature(t *testing.T) {
	tests := []struct {
		name        string
		signature   string
		wantUpgrade bool
	}{
		{"valid wss signature", twilioSignature(testAuthToken, testMediaURL, nil), true},
		{"missing signature", "", false},
		{"wrong signature", "bm90LWEtc2lnbmF0dXJl", false},
		{"signed with https URL", twilioSignature(testAuthToken, testBaseURL+"/twilio/media", nil), false},
		{"wrong auth token", twilioSignature("other-token", testMediaURL, nil), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, router := newTestRouter(&fakeCalls{}, newFakeTranscription())
			srv := httptest.NewServer(router)
			defer srv.Close()

			header := http.Header{}
			if tt.signature != "" {
				header.Set("X-Twilio-Signature", tt.signature)
			}
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), header)
			if resp != nil && resp.Body != nil {
				defer resp.Body.Close()
			}
			if tt.wantUpgrade {
				require.NoError(t, err)
				assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
				require.NoError(t, conn.Close())
				return
			}
			require.ErrorIs(t, err, websocket.ErrBadHandshake)
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}
}

func TestNewHandler(t *testing.T) {
	t.Run("trailing slash and media URL", func(t *testing.T) {
		cfg := testConfig()
		cfg.PublicBaseURL = testBaseURL + "/"
		h := NewHandler(cfg, &fakeCalls{}, newFakeTranscription(), nil)
		assert.Equal(t, testBaseURL, h.cfg.PublicBaseURL)
		assert.Equal(t, testMediaURL, h.mediaURL)
	})
	t.Run("unparseable base URL does not panic", func(t *testing.T) {
		cfg := testConfig()
		cfg.PublicBaseURL = "https://bad host\x7f"
		h := NewHandler(cfg, &fakeCalls{}, newFakeTranscription(), nil)
		assert.Equal(t, "wss:///twilio/media", h.mediaURL)
		h.Register(mux.NewRouter())
	})
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/twilio/media"
}
