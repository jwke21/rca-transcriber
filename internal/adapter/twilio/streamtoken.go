package twilio

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// signStreamToken binds the incident and engineer IDs passed as <Parameter>s
// to the call: base64url(HMAC-SHA256(secret, "{incident_id}:{engineer_id}:{CallSid}"))
// with no padding.
func signStreamToken(secret string, incidentID, engineerID int64, callSID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d:%d:%s", incidentID, engineerID, callSID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyStreamToken reports whether token was signed for these IDs and call,
// using a constant-time comparison.
func verifyStreamToken(secret string, incidentID, engineerID int64, callSID, token string) bool {
	expected := signStreamToken(secret, incidentID, engineerID, callSID)
	return hmac.Equal([]byte(expected), []byte(token))
}
