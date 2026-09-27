package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

const (
	testAccountSID    = "AC00000000000000000000000000000000"
	testAuthToken     = "test-auth-token"
	testBaseURL       = "https://rca.example.ngrok.app"
	testMediaURL      = "wss://rca.example.ngrok.app/twilio/media"
	testStreamSecret  = "0123456789abcdef0123456789abcdef"
	testCallSID       = "CA11111111111111111111111111111111"
	testCallerPhone   = "+15555550123"
	testEngineerID    = int64(7)
	testIncidentID    = int64(4821)
	testIncidentDigit = "4821"
)

// --- CallService fake ---

type fakeCalls struct {
	mu    sync.Mutex
	calls []string

	engineer     domain.Engineer
	authorizeErr error
	begin        port.RecordingStart
	beginErr     error
	resume       port.RecordingStart
	resumeErr    error
	confirmed    bool
	confirmErr   error

	gotPhone      string
	gotDigits     string
	gotIncidentID int64
}

var _ port.CallService = (*fakeCalls)(nil)

func (f *fakeCalls) record(name, phone, digits string, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.gotPhone, f.gotDigits, f.gotIncidentID = phone, digits, id
}

func (f *fakeCalls) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeCalls) AuthorizeCaller(_ context.Context, phone string) (domain.Engineer, error) {
	f.record("AuthorizeCaller", phone, "", 0)
	return f.engineer, f.authorizeErr
}

func (f *fakeCalls) BeginRecording(_ context.Context, phone, digits string) (port.RecordingStart, error) {
	f.record("BeginRecording", phone, digits, 0)
	return f.begin, f.beginErr
}

func (f *fakeCalls) ResumeRecording(_ context.Context, phone string, id int64) (port.RecordingStart, error) {
	f.record("ResumeRecording", phone, "", id)
	return f.resume, f.resumeErr
}

func (f *fakeCalls) ConfirmResolution(_ context.Context, phone string, id int64, digits string) (bool, error) {
	f.record("ConfirmResolution", phone, digits, id)
	return f.confirmed, f.confirmErr
}

// --- TranscriptionService and MediaSession fakes ---

type startArgs struct {
	incidentID, engineerID int64
}

type fakeTranscription struct {
	session  *fakeSession
	startErr error
	started  chan startArgs

	mu     sync.Mutex
	starts int
}

var _ port.TranscriptionService = (*fakeTranscription)(nil)

func newFakeTranscription() *fakeTranscription {
	return &fakeTranscription{session: newFakeSession(), started: make(chan startArgs, 10)}
}

func (f *fakeTranscription) StartSession(_ context.Context, incidentID, engineerID int64) (port.MediaSession, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	f.started <- startArgs{incidentID, engineerID}
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.session, nil
}

func (f *fakeTranscription) Starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

type fakeSession struct {
	audio chan []byte
	dtmf  chan string
	// endDigits maps a digit to the endStream result of HandleDTMF.
	endDigits map[string]bool
	dtmfErr   error
	audioErr  error

	mu             sync.Mutex
	endCount       int
	endHadDeadline bool
	endCtxErr      error
	ended          chan struct{}
}

var _ port.MediaSession = (*fakeSession)(nil)

func newFakeSession() *fakeSession {
	return &fakeSession{
		audio:     make(chan []byte, 100),
		dtmf:      make(chan string, 10),
		endDigits: map[string]bool{"#": true},
		ended:     make(chan struct{}),
	}
}

func (f *fakeSession) HandleAudio(_ context.Context, mulaw []byte) error {
	f.audio <- append([]byte(nil), mulaw...)
	return f.audioErr
}

func (f *fakeSession) HandleDTMF(_ context.Context, digit string) (bool, error) {
	f.dtmf <- digit
	return f.endDigits[digit], f.dtmfErr
}

func (f *fakeSession) End(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endCount++
	if f.endCount == 1 {
		_, f.endHadDeadline = ctx.Deadline()
		f.endCtxErr = ctx.Err()
		close(f.ended)
	}
	return nil
}

// EndCtx reports whether the first End call had a deadline and whether its
// ctx was already done.
func (f *fakeSession) EndCtx() (hadDeadline bool, ctxErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endHadDeadline, f.endCtxErr
}

func (f *fakeSession) EndCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endCount
}

// --- helpers ---

func testConfig() Config {
	return Config{
		AccountSID:          testAccountSID,
		AuthToken:           testAuthToken,
		PublicBaseURL:       testBaseURL,
		StreamSigningSecret: testStreamSecret,
	}
}

func newTestRouter(calls port.CallService, transcription port.TranscriptionService) (*Handler, *mux.Router) {
	h := NewHandler(testConfig(), calls, transcription, nil)
	r := mux.NewRouter()
	h.Register(r)
	return h, r
}

// twilioSignature computes X-Twilio-Signature independently of twilio-go:
// base64(HMAC-SHA1(authToken, url + sorted key+value pairs)).
func twilioSignature(authToken, fullURL string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params[k])
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhookParams returns the form Twilio sends, plus extra.
func webhookParams(extra map[string]string) map[string]string {
	p := map[string]string{
		"AccountSid": testAccountSID,
		"CallSid":    testCallSID,
		"From":       testCallerPhone,
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// signedRequest builds a webhook POST to requestURI signed the way Twilio does.
func signedRequest(requestURI string, params map[string]string) *http.Request {
	return rawRequest(requestURI, params, twilioSignature(testAuthToken, testBaseURL+requestURI, params))
}

func rawRequest(requestURI string, params map[string]string, signature string) *http.Request {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodPost, requestURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if signature != "" {
		req.Header.Set("X-Twilio-Signature", signature)
	}
	return req
}

// xmlNode is a generic parsed XML element, for asserting TwiML structure.
type xmlNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []xmlNode  `xml:",any"`
}

func (n xmlNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n xmlNode) names() []string {
	out := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		out = append(out, c.XMLName.Local)
	}
	return out
}

func (n xmlNode) child(t *testing.T, name string) xmlNode {
	t.Helper()
	for _, c := range n.Children {
		if c.XMLName.Local == name {
			return c
		}
	}
	require.Failf(t, "missing child", "<%s> has no <%s> child: %v", n.XMLName.Local, name, n.names())
	return xmlNode{}
}

func parseTwiML(t *testing.T, rec *httptest.ResponseRecorder) xmlNode {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/xml; charset=utf-8", rec.Header().Get("Content-Type"))
	var root xmlNode
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &root), rec.Body.String())
	require.Equal(t, "Response", root.XMLName.Local)
	return root
}
