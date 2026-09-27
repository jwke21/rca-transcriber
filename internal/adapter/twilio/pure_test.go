package twilio

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeakDigits(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{4821, "4 8 2 1"},
		{7, "7"},
		{100, "1 0 0"},
		{999999999, "9 9 9 9 9 9 9 9 9"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, speakDigits(tt.in))
		})
	}
}

func TestStreamToken(t *testing.T) {
	token := signStreamToken(testStreamSecret, testIncidentID, testEngineerID, testCallSID)

	t.Run("format is unpadded base64url of a SHA-256 MAC", func(t *testing.T) {
		assert.NotContains(t, token, "=")
		assert.False(t, strings.ContainsAny(token, "+/"))
		raw, err := base64.RawURLEncoding.DecodeString(token)
		require.NoError(t, err)
		assert.Len(t, raw, 32)
	})

	t.Run("deterministic", func(t *testing.T) {
		assert.Equal(t, token, signStreamToken(testStreamSecret, testIncidentID, testEngineerID, testCallSID))
	})

	tests := []struct {
		name       string
		secret     string
		incidentID int64
		engineerID int64
		callSID    string
		token      string
		want       bool
	}{
		{"round trip", testStreamSecret, testIncidentID, testEngineerID, testCallSID, token, true},
		{"tampered incident", testStreamSecret, testIncidentID + 1, testEngineerID, testCallSID, token, false},
		{"tampered engineer", testStreamSecret, testIncidentID, testEngineerID + 1, testCallSID, token, false},
		{"different call", testStreamSecret, testIncidentID, testEngineerID, "CA22222222222222222222222222222222", token, false},
		{"different secret", "another-secret-another-secret-xx", testIncidentID, testEngineerID, testCallSID, token, false},
		{"empty token", testStreamSecret, testIncidentID, testEngineerID, testCallSID, "", false},
		{"padded token", testStreamSecret, testIncidentID, testEngineerID, testCallSID, token + "=", false},
		{"swapped IDs", testStreamSecret, testEngineerID, testIncidentID, testCallSID, token, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, verifyStreamToken(tt.secret, tt.incidentID, tt.engineerID, tt.callSID, tt.token))
		})
	}
}
