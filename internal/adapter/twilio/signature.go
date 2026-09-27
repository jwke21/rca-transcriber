package twilio

import (
	"net/http"
)

// requireWebhookSignature rejects, with 403 and an empty body, any webhook
// whose X-Twilio-Signature doesn't match or whose AccountSid isn't ours. The
// signed URL is PublicBaseURL plus the request URI (never rebuilt from Host,
// because ngrok proxies the request), and the params are the POST form only.
func (h *Handler) requireWebhookSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, webhookMaxBodyBytes)
		if err := r.ParseForm(); err != nil {
			h.logger.WarnContext(r.Context(), "twilio: webhook rejected: unreadable form", "path", r.URL.Path, "error", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}

		params := make(map[string]string, len(r.PostForm))
		for k, v := range r.PostForm {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}

		signedURL := h.cfg.PublicBaseURL + r.URL.RequestURI()
		signature := r.Header.Get(twilioSignatureHeader)
		if signature == "" || !h.validator.Validate(signedURL, params, signature) {
			h.logger.WarnContext(r.Context(), "twilio: webhook rejected: bad signature", "path", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.PostForm.Get("AccountSid") != h.cfg.AccountSID {
			h.logger.WarnContext(r.Context(), "twilio: webhook rejected: account SID mismatch", "path", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// validUpgradeSignature checks X-Twilio-Signature on the media WebSocket
// upgrade. Twilio signs the wss:// form of the URL with no params.
func (h *Handler) validUpgradeSignature(r *http.Request) bool {
	signature := r.Header.Get(twilioSignatureHeader)
	return signature != "" && h.validator.Validate(h.mediaURL, map[string]string{}, signature)
}
