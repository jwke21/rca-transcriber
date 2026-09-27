package twilio

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

// waitTimeout bounds every wait on a channel in these tests.
const waitTimeout = 5 * time.Second

type mediaHarness struct {
	t    *testing.T
	tr   *fakeTranscription
	sess *fakeSession
	conn *websocket.Conn
}

// newMediaHarness starts a server with h and dials a signed media WebSocket.
func newMediaHarness(t *testing.T, configure func(h *Handler, tr *fakeTranscription)) *mediaHarness {
	t.Helper()
	tr := newFakeTranscription()
	h := NewHandler(testConfig(), &fakeCalls{}, tr, nil)
	if configure != nil {
		configure(h, tr)
	}
	r := mux.NewRouter()
	h.Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	header := http.Header{}
	header.Set("X-Twilio-Signature", twilioSignature(testAuthToken, testMediaURL, nil))
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), header)
	require.NoError(t, err)
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })
	return &mediaHarness{t: t, tr: tr, sess: tr.session, conn: conn}
}

func (m *mediaHarness) send(v any) {
	m.t.Helper()
	require.NoError(m.t, m.conn.WriteJSON(v))
}

func (m *mediaHarness) sendRaw(msgType int, data []byte) {
	m.t.Helper()
	require.NoError(m.t, m.conn.WriteMessage(msgType, data))
}

func startMsg(incidentID, engineerID, token, callSID string) map[string]any {
	return map[string]any{
		"event":          "start",
		"sequenceNumber": "1",
		"streamSid":      "MZ123",
		"start": map[string]any{
			"streamSid":  "MZ123",
			"accountSid": testAccountSID,
			"callSid":    callSID,
			"tracks":     []string{"inbound"},
			"customParameters": map[string]string{
				"incident_id": incidentID,
				"engineer_id": engineerID,
				"token":       token,
			},
			"mediaFormat": map[string]any{"encoding": "audio/x-mulaw", "sampleRate": 8000, "channels": 1},
		},
	}
}

func validStart() map[string]any {
	token := signStreamToken(testStreamSecret, testIncidentID, testEngineerID, testCallSID)
	return startMsg("4821", "7", token, testCallSID)
}

func mediaMsg(track string, payload []byte) map[string]any {
	return mediaMsgRaw(track, base64.StdEncoding.EncodeToString(payload))
}

func mediaMsgRaw(track, payload string) map[string]any {
	return map[string]any{
		"event": "media",
		"media": map[string]any{"track": track, "chunk": "1", "timestamp": "5", "payload": payload},
	}
}

func dtmfMsg(digit string) map[string]any {
	return map[string]any{"event": "dtmf", "dtmf": map[string]any{"track": "inbound_track", "digit": digit}}
}

func stopMsg() map[string]any {
	return map[string]any{"event": "stop", "stop": map[string]any{"callSid": testCallSID}}
}

// waitStarted waits for StartSession and returns its args.
func (m *mediaHarness) waitStarted() startArgs {
	m.t.Helper()
	select {
	case a := <-m.tr.started:
		return a
	case <-time.After(waitTimeout):
		require.FailNow(m.t, "StartSession not called")
		return startArgs{}
	}
}

func (m *mediaHarness) waitEnded() {
	m.t.Helper()
	select {
	case <-m.sess.ended:
	case <-time.After(waitTimeout):
		require.FailNow(m.t, "End not called")
	}
}

func (m *mediaHarness) nextAudio() []byte {
	m.t.Helper()
	select {
	case a := <-m.sess.audio:
		return a
	case <-time.After(waitTimeout):
		require.FailNow(m.t, "HandleAudio not called")
		return nil
	}
}

func (m *mediaHarness) nextDTMF() string {
	m.t.Helper()
	select {
	case d := <-m.sess.dtmf:
		return d
	case <-time.After(waitTimeout):
		require.FailNow(m.t, "HandleDTMF not called")
		return ""
	}
}

// waitServerClose reads until the server closes the connection and returns the error.
func (m *mediaHarness) waitServerClose() error {
	m.t.Helper()
	require.NoError(m.t, m.conn.SetReadDeadline(time.Now().Add(waitTimeout)))
	for {
		if _, _, err := m.conn.ReadMessage(); err != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				require.FailNow(m.t, "server did not close the connection")
			}
			return err
		}
	}
}

// drainAudio returns every frame HandleAudio has received so far.
func (m *mediaHarness) drainAudio() [][]byte {
	var out [][]byte
	for {
		select {
		case a := <-m.sess.audio:
			out = append(out, a)
		default:
			return out
		}
	}
}

func TestMediaStart(t *testing.T) {
	t.Run("valid start calls StartSession with the right IDs", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(map[string]any{"event": "connected", "protocol": "Call", "version": "1.0.0"})
		m.send(validStart())
		assert.Equal(t, startArgs{incidentID: testIncidentID, engineerID: testEngineerID}, m.waitStarted())

		m.send(stopMsg())
		m.waitEnded()
		_ = m.waitServerClose()
	})

	token := signStreamToken(testStreamSecret, testIncidentID, testEngineerID, testCallSID)
	bad := []struct {
		name string
		msg  map[string]any
	}{
		{"wrong token", startMsg("4821", "7", "bm9wZQ", testCallSID)},
		{"tampered incident", startMsg("4822", "7", token, testCallSID)},
		{"tampered engineer", startMsg("4821", "8", token, testCallSID)},
		{"different call", startMsg("4821", "7", token, "CA22222222222222222222222222222222")},
		{"missing token", startMsg("4821", "7", "", testCallSID)},
		{"non-numeric incident", startMsg("abc", "7", token, testCallSID)},
		{"non-numeric engineer", startMsg("4821", "x", token, testCallSID)},
		{"zero engineer", startMsg("4821", "0", signStreamToken(testStreamSecret, testIncidentID, 0, testCallSID), testCallSID)},
		{"no start block", map[string]any{"event": "start"}},
	}
	for _, tt := range bad {
		t.Run(tt.name+" closes without a session", func(t *testing.T) {
			m := newMediaHarness(t, nil)
			m.send(tt.msg)
			err := m.waitServerClose()
			assert.True(t, websocket.IsCloseError(err, websocket.ClosePolicyViolation), "got %v", err)
			assert.Zero(t, m.tr.Starts())
			assert.Zero(t, m.sess.EndCount())
		})
	}

	t.Run("StartSession error closes the connection", func(t *testing.T) {
		m := newMediaHarness(t, func(_ *Handler, tr *fakeTranscription) { tr.startErr = errBoom })
		m.send(validStart())
		m.waitStarted()
		err := m.waitServerClose()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseInternalServerErr), "got %v", err)
		assert.Zero(t, m.sess.EndCount())
	})

	t.Run("duplicate valid start is ignored", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		m.send(validStart())
		m.send(mediaMsg("inbound", []byte("after")))
		assert.Equal(t, []byte("after"), m.nextAudio())
		assert.Equal(t, 1, m.tr.Starts())

		m.send(stopMsg())
		m.waitEnded()
	})

	t.Run("bad token after start closes and ends the session", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		m.send(startMsg("4821", "7", "forged", testCallSID))
		err := m.waitServerClose()
		assert.True(t, websocket.IsCloseError(err, websocket.ClosePolicyViolation), "got %v", err)
		m.waitEnded()
		assert.Equal(t, 1, m.sess.EndCount())
	})
}

func TestMediaAudio(t *testing.T) {
	t.Run("inbound frames are decoded and forwarded in order", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()

		m.send(mediaMsg("inbound", []byte{0x01, 0x02}))
		m.send(mediaMsg("outbound", []byte("dropped")))
		m.send(mediaMsg("inbound", []byte{0x03}))
		m.send(mediaMsgRaw("inbound", "!!!not base64!!!"))
		m.send(map[string]any{"event": "media"})
		m.send(mediaMsg("inbound", []byte{0x04, 0x05, 0x06}))
		m.send(stopMsg())
		m.waitEnded()

		assert.Equal(t, [][]byte{{0x01, 0x02}, {0x03}, {0x04, 0x05, 0x06}}, m.drainAudio())
	})

	t.Run("media before start is ignored", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(mediaMsg("inbound", []byte("early")))
		m.send(dtmfMsg("#"))
		m.send(validStart())
		m.waitStarted()
		m.send(mediaMsg("inbound", []byte("late")))
		m.send(stopMsg())
		m.waitEnded()

		assert.Equal(t, [][]byte{[]byte("late")}, m.drainAudio())
		assert.Empty(t, m.sess.dtmf)
	})

	t.Run("HandleAudio errors do not stop the loop", func(t *testing.T) {
		m := newMediaHarness(t, func(_ *Handler, tr *fakeTranscription) { tr.session.audioErr = domain.ErrSessionEnded })
		m.send(validStart())
		m.waitStarted()
		m.send(mediaMsg("inbound", []byte{1}))
		m.send(mediaMsg("inbound", []byte{2}))
		assert.Equal(t, []byte{1}, m.nextAudio())
		assert.Equal(t, []byte{2}, m.nextAudio())
	})

	t.Run("malformed JSON and binary frames do not kill the loop", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.sendRaw(websocket.TextMessage, []byte("{not json"))
		m.sendRaw(websocket.BinaryMessage, []byte{0xff})
		m.send(map[string]any{"event": "mark", "mark": map[string]any{"name": "x"}})
		m.send(map[string]any{"event": "something-new"})
		m.send(validStart())
		m.waitStarted()
		m.sendRaw(websocket.TextMessage, []byte(`{"event":"media","media":`))
		m.send(mediaMsg("inbound", []byte("ok")))
		assert.Equal(t, []byte("ok"), m.nextAudio())
		m.send(stopMsg())
		m.waitEnded()
	})
}

func TestMediaDTMF(t *testing.T) {
	t.Run("pound with endStream closes the connection normally", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		m.send(dtmfMsg("#"))
		assert.Equal(t, "#", m.nextDTMF())

		err := m.waitServerClose()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "got %v", err)
		m.waitEnded()
		assert.Equal(t, 1, m.sess.EndCount())
	})

	t.Run("other digits keep the stream open", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		m.send(dtmfMsg("1"))
		m.send(dtmfMsg("*"))
		assert.Equal(t, "1", m.nextDTMF())
		assert.Equal(t, "*", m.nextDTMF())

		// The loop is still alive: audio after the digits is forwarded.
		m.send(mediaMsg("inbound", []byte("still here")))
		assert.Equal(t, []byte("still here"), m.nextAudio())
		assert.Zero(t, m.sess.EndCount())

		m.send(stopMsg())
		m.waitEnded()
	})

	t.Run("HandleDTMF error without endStream keeps the stream open", func(t *testing.T) {
		m := newMediaHarness(t, func(_ *Handler, tr *fakeTranscription) {
			tr.session.dtmfErr = errBoom
			tr.session.endDigits = map[string]bool{}
		})
		m.send(validStart())
		m.waitStarted()
		m.send(dtmfMsg("#"))
		assert.Equal(t, "#", m.nextDTMF())
		m.send(mediaMsg("inbound", []byte("x")))
		assert.Equal(t, []byte("x"), m.nextAudio())
	})

	t.Run("HandleDTMF error with endStream still closes", func(t *testing.T) {
		m := newMediaHarness(t, func(_ *Handler, tr *fakeTranscription) { tr.session.dtmfErr = errBoom })
		m.send(validStart())
		m.waitStarted()
		m.send(dtmfMsg("#"))
		err := m.waitServerClose()
		assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "got %v", err)
		m.waitEnded()
	})
}

func TestMediaEnd(t *testing.T) {
	t.Run("stop calls End exactly once and closes", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		m.send(stopMsg())
		m.waitEnded()
		_ = m.waitServerClose()
		assert.Equal(t, 1, m.sess.EndCount())

		hadDeadline, ctxErr := m.sess.EndCtx()
		assert.True(t, hadDeadline, "End must get a bounded ctx")
		assert.NoError(t, ctxErr)
	})

	t.Run("stop before start closes without End", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(stopMsg())
		_ = m.waitServerClose()
		assert.Zero(t, m.tr.Starts())
		assert.Zero(t, m.sess.EndCount())
	})

	t.Run("abrupt client disconnect calls End with a live ctx", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		require.NoError(t, m.conn.NetConn().Close())
		m.waitEnded()
		assert.Equal(t, 1, m.sess.EndCount())

		hadDeadline, ctxErr := m.sess.EndCtx()
		assert.True(t, hadDeadline)
		assert.NoError(t, ctxErr, "End must not inherit the cancelled request ctx")
	})

	t.Run("client close frame calls End", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		require.NoError(t, m.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))
		m.waitEnded()
		assert.Equal(t, 1, m.sess.EndCount())
	})

	t.Run("read deadline calls End", func(t *testing.T) {
		m := newMediaHarness(t, func(h *Handler, _ *fakeTranscription) { h.readTimeout = 200 * time.Millisecond })
		m.send(validStart())
		m.waitStarted()
		// Send nothing more: the server's read deadline expires.
		m.waitEnded()
		_ = m.waitServerClose()
		assert.Equal(t, 1, m.sess.EndCount())
	})

	t.Run("oversized frame ends the stream", func(t *testing.T) {
		m := newMediaHarness(t, nil)
		m.send(validStart())
		m.waitStarted()
		big, err := json.Marshal(mediaMsg("inbound", make([]byte, mediaReadLimit)))
		require.NoError(t, err)
		// The server may close before the whole frame is written, so the
		// write error is irrelevant here.
		_ = m.conn.WriteMessage(websocket.TextMessage, big)
		m.waitEnded()
		assert.Equal(t, 1, m.sess.EndCount())
	})
}
