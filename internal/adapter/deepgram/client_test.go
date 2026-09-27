package deepgram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

// capturedRequest holds what the fake Deepgram server observed about the
// handshake request, captured before the Upgrade call (the *http.Request is
// not safe to read from concurrently once handling continues).
type capturedRequest struct {
	Header http.Header
	Query  url.Values
}

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsURL rewrites an httptest server's http:// URL to ws://.
func wsURL(t *testing.T, httpURL string) string {
	t.Helper()
	u, err := url.Parse(httpURL)
	require.NoError(t, err)
	u.Scheme = "ws"
	return u.String()
}

// resultJSON builds a Deepgram "Results" message text frame.
func resultJSON(isFinal bool, start, duration float64, transcript string) string {
	type alternative struct {
		Transcript string `json:"transcript"`
	}
	type channel struct {
		Alternatives []alternative `json:"alternatives"`
	}
	msg := struct {
		Type     string  `json:"type"`
		IsFinal  bool    `json:"is_final"`
		Start    float64 `json:"start"`
		Duration float64 `json:"duration"`
		Channel  channel `json:"channel"`
	}{
		Type:     "Results",
		IsFinal:  isFinal,
		Start:    start,
		Duration: duration,
		Channel:  channel{Alternatives: []alternative{{Transcript: transcript}}},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestOpen_HandshakeHeaderAndQueryParams(t *testing.T) {
	captured := make(chan capturedRequest, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- capturedRequest{Header: r.Header.Clone(), Query: r.URL.Query()}
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client := New(Config{
		APIKey: "super-secret-key-12345",
		Model:  "nova-2",
		URL:    wsURL(t, srv.URL),
	}, nil)

	stream, err := client.Open(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	select {
	case req := <-captured:
		assert.Equal(t, "Token super-secret-key-12345", req.Header.Get("Authorization"))
		assert.Equal(t, "nova-2", req.Query.Get("model"))
		assert.Equal(t, "mulaw", req.Query.Get("encoding"))
		assert.Equal(t, "8000", req.Query.Get("sample_rate"))
		assert.Equal(t, "1", req.Query.Get("channels"))
		assert.Equal(t, "true", req.Query.Get("punctuate"))
		assert.Equal(t, "true", req.Query.Get("smart_format"))
		assert.Equal(t, "false", req.Query.Get("interim_results"))
	case <-time.After(2 * time.Second):
		t.Fatal("server never received handshake request")
	}
}

func TestOpen_DialFailure_DoesNotLeakAPIKey(t *testing.T) {
	const apiKey = "super-secret-key-should-not-leak"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := New(Config{
		APIKey: apiKey,
		Model:  "nova-2",
		URL:    wsURL(t, srv.URL),
	}, nil)

	stream, err := client.Open(context.Background())
	require.Error(t, err)
	require.Nil(t, stream)
	assert.Contains(t, err.Error(), "401")
	assert.NotContains(t, err.Error(), apiKey)
}

func TestReadLoop_FiltersAndSkipsMalformed(t *testing.T) {
	serverDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		defer close(serverDone)

		// Malformed JSON: reader must not stop.
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{not valid json`)))
		// Non-final result: skipped.
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(resultJSON(false, 1, 1, "interim words"))))
		// Blank transcript: skipped.
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(resultJSON(true, 1, 1, ""))))
		// Unknown type: ignored.
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"Metadata"}`)))
		// Valid final result: emitted.
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(resultJSON(true, 1.5, 2.25, "hello world"))))
	}))
	defer srv.Close()

	fixedNow := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil, WithClock(func() time.Time { return fixedNow }))

	stream, err := client.Open(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	select {
	case seg, ok := <-stream.Results():
		require.True(t, ok)
		assert.Equal(t, "hello world", seg.Text)
		assert.True(t, seg.IsFinal)
		assert.Equal(t, fixedNow.Add(1500*time.Millisecond), seg.SpokenAt)
		assert.Equal(t, 2250*time.Millisecond, seg.Duration)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive expected segment")
	}

	<-serverDone
}

func TestSendAudio_BinaryFrameByteForByte(t *testing.T) {
	received := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.BinaryMessage {
			received <- data
		}
	}))
	defer srv.Close()

	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
	stream, err := client.Open(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	payload := []byte{0x00, 0x7f, 0xff, 0x10, 0x20, 0x30}
	require.NoError(t, stream.SendAudio(payload))

	select {
	case got := <-received:
		assert.Equal(t, payload, got)
	case <-time.After(2 * time.Second):
		t.Fatal("server never received audio frame")
	}
}

func TestFinish_SendsFinalizeThenCloseStream_AndResultsClosesAfterLastResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		_, first, err := conn.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"Finalize"}`, string(first))

		_, second, err := conn.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"CloseStream"}`, string(second))

		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(resultJSON(true, 3, 1, "final words"))))
		// Closing the connection simulates Deepgram closing after CloseStream.
	}))
	defer srv.Close()

	fixedNow := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil, WithClock(func() time.Time { return fixedNow }))
	stream, err := client.Open(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, stream.Finish(ctx))

	seg, ok := <-stream.Results()
	require.True(t, ok)
	assert.Equal(t, "final words", seg.Text)
	assert.Equal(t, fixedNow.Add(3*time.Second), seg.SpokenAt)

	_, ok = <-stream.Results()
	assert.False(t, ok, "Results should be closed after the last result")
}

func TestFinish_CtxTimeout_ForceCloses(t *testing.T) {
	serverReceived := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		// Read Finalize and CloseStream but never respond or close, to force
		// the client's ctx to expire.
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}
		close(serverReceived)

		// Block until the client forcibly closes the connection.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
	stream, err := client.Open(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = stream.Finish(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	<-serverReceived

	// The connection was force-closed, so a subsequent SendAudio fails.
	err = stream.SendAudio([]byte{1, 2, 3})
	assert.ErrorIs(t, err, domain.ErrStreamClosed)
}

func TestSendAudio_AfterFinishOrClose_ReturnsErrStreamClosed(t *testing.T) {
	t.Run("after Finish", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := testUpgrader.Upgrade(w, r, nil)
			require.NoError(t, err)
			defer conn.Close()
			for i := 0; i < 2; i++ {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
			// Close after receiving Finalize + CloseStream.
		}))
		defer srv.Close()

		client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
		stream, err := client.Open(context.Background())
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, stream.Finish(ctx))

		err = stream.SendAudio([]byte{1})
		assert.ErrorIs(t, err, domain.ErrStreamClosed)
	})

	t.Run("after Close", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := testUpgrader.Upgrade(w, r, nil)
			require.NoError(t, err)
			defer conn.Close()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}))
		defer srv.Close()

		client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
		stream, err := client.Open(context.Background())
		require.NoError(t, err)

		require.NoError(t, stream.Close())

		err = stream.SendAudio([]byte{1})
		assert.ErrorIs(t, err, domain.ErrStreamClosed)
	})
}

func TestMidStreamServerDrop_ClosesResultsAndFailsSendAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		// Drop the connection immediately without reading or writing anything else.
		conn.Close()
	}))
	defer srv.Close()

	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
	stream, err := client.Open(context.Background())
	require.NoError(t, err)

	_, ok := <-stream.Results()
	assert.False(t, ok, "Results should close when the server drops the connection")

	err = stream.SendAudio([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestClose_Idempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client := New(Config{APIKey: "k", Model: "nova-2", URL: wsURL(t, srv.URL)}, nil)
	stream, err := client.Open(context.Background())
	require.NoError(t, err)

	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())
}

func TestNew_DefaultURLAndOptions(t *testing.T) {
	client := New(Config{APIKey: "k", Model: "nova-2"}, nil)
	require.NotNil(t, client)
	assert.Equal(t, "", client.cfg.URL) // default is applied at dial time, not construction

	u, err := buildURL(client.cfg)
	require.NoError(t, err)
	assert.Equal(t, defaultURL, strings.SplitN(u.String(), "?", 2)[0])
}
