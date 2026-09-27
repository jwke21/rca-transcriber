package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gh "github.com/google/go-github/v66/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

const (
	testToken   = "ghp_supersecrettesttoken123"
	testOwner   = "acme"
	testRepo    = "rca"
	testBase    = "main"
	testBaseSHA = "basesha123"
	testFileSHA = "filesha456"
	testPRURL   = "https://github.com/acme/rca/pull/7"
	testOldURL  = "https://github.com/acme/rca/pull/3"
)

// Step names used to route and record requests.
const (
	stepGetRef     = "get ref"
	stepCreateRef  = "create ref"
	stepGetFile    = "get file"
	stepPutFile    = "put file"
	stepListPRs    = "list prs"
	stepEditPR     = "edit pr"
	stepCreatePR   = "create pr"
	stepUnexpected = "unexpected"
)

func testDoc() domain.RCADocument {
	return domain.RCADocument{
		IncidentID: 4821,
		Branch:     "incident-4821",
		Path:       "incidents/2026-09-27-4821.md",
		Title:      "Incident 4821: Payments API errors",
		Body:       "Generated RCA for incident 4821. Please review.",
		Content:    "# Incident 4821: Payments API errors\n\n## Summary\n\nThings broke.\n",
	}
}

// recordedRequest is one request the fake GitHub server received.
type recordedRequest struct {
	Step  string
	Auth  string
	Query map[string]string
	Body  map[string]any
}

// response is a canned status and JSON body.
type response struct {
	status int
	body   string
}

// fakeGitHub is an httptest-backed GitHub REST API. Each step returns its
// configured response, or a default happy-path response.
type fakeGitHub struct {
	t         *testing.T
	server    *httptest.Server
	responses map[string]response

	mu       sync.Mutex
	requests []recordedRequest
}

func newFakeGitHub(t *testing.T, overrides map[string]response) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{
		t: t,
		responses: map[string]response{
			stepGetRef:    {http.StatusOK, fmt.Sprintf(`{"ref":"refs/heads/main","object":{"sha":%q,"type":"commit"}}`, testBaseSHA)},
			stepCreateRef: {http.StatusCreated, `{"ref":"refs/heads/incident-4821","object":{"sha":"basesha123"}}`},
			stepGetFile:   {http.StatusNotFound, `{"message":"Not Found"}`},
			stepPutFile:   {http.StatusCreated, `{"content":{"sha":"newsha"},"commit":{"sha":"commitsha"}}`},
			stepListPRs:   {http.StatusOK, `[]`},
			stepEditPR:    {http.StatusOK, fmt.Sprintf(`{"number":3,"html_url":%q}`, testOldURL)},
			stepCreatePR:  {http.StatusCreated, fmt.Sprintf(`{"number":7,"html_url":%q}`, testPRURL)},
		},
	}
	for k, v := range overrides {
		f.responses[k] = v
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func route(r *http.Request) string {
	const prefix = "/repos/" + testOwner + "/" + testRepo + "/"
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == prefix+"git/ref/heads/"+testBase:
		return stepGetRef
	case r.Method == http.MethodPost && p == prefix+"git/refs":
		return stepCreateRef
	case r.Method == http.MethodGet && p == prefix+"contents/incidents/2026-09-27-4821.md":
		return stepGetFile
	case r.Method == http.MethodPut && p == prefix+"contents/incidents/2026-09-27-4821.md":
		return stepPutFile
	case r.Method == http.MethodGet && p == prefix+"pulls":
		return stepListPRs
	case r.Method == http.MethodPatch && strings.HasPrefix(p, prefix+"pulls/"):
		return stepEditPR
	case r.Method == http.MethodPost && p == prefix+"pulls":
		return stepCreatePR
	}
	return stepUnexpected
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	step := route(r)
	rec := recordedRequest{Step: step, Auth: r.Header.Get("Authorization"), Query: map[string]string{}}
	for k := range r.URL.Query() {
		rec.Query[k] = r.URL.Query().Get(k)
	}
	if raw, _ := io.ReadAll(r.Body); len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &rec.Body)
	}
	if step == stepEditPR {
		rec.Query["path"] = r.URL.Path
	}

	f.mu.Lock()
	f.requests = append(f.requests, rec)
	resp, ok := f.responses[step]
	f.mu.Unlock()

	if !ok || step == stepUnexpected {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusTeapot)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (f *fakeGitHub) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeGitHub) steps() []string {
	var out []string
	for _, r := range f.recorded() {
		out = append(out, r.Step)
	}
	return out
}

func (f *fakeGitHub) request(t *testing.T, step string) recordedRequest {
	t.Helper()
	for _, r := range f.recorded() {
		if r.Step == step {
			return r
		}
	}
	require.Failf(t, "request not found", "no %q request recorded", step)
	return recordedRequest{}
}

func newTestPublisher(t *testing.T, f *fakeGitHub, logger *slog.Logger) *Publisher {
	t.Helper()
	p, err := New(Config{
		Token:      testToken,
		Owner:      testOwner,
		Repo:       testRepo,
		BaseBranch: testBase,
		APIBaseURL: f.server.URL, // no trailing slash: New must add it
	}, logger)
	require.NoError(t, err)
	return p
}

func decodeContent(t *testing.T, body map[string]any) string {
	t.Helper()
	enc, ok := body["content"].(string)
	require.True(t, ok, "content must be a string")
	raw, err := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, err)
	return string(raw)
}

func TestPublish_FreshPublish(t *testing.T) {
	f := newFakeGitHub(t, nil)
	var logs bytes.Buffer
	p := newTestPublisher(t, f, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	doc := testDoc()

	url, err := p.Publish(context.Background(), doc)
	require.NoError(t, err)
	assert.Equal(t, testPRURL, url)

	assert.Equal(t, []string{stepGetRef, stepCreateRef, stepGetFile, stepPutFile, stepListPRs, stepCreatePR}, f.steps())

	for _, r := range f.recorded() {
		assert.Equal(t, "Bearer "+testToken, r.Auth, "auth header on %s", r.Step)
	}

	createRef := f.request(t, stepCreateRef)
	assert.Equal(t, "refs/heads/incident-4821", createRef.Body["ref"])
	assert.Equal(t, testBaseSHA, createRef.Body["sha"])

	getFile := f.request(t, stepGetFile)
	assert.Equal(t, doc.Branch, getFile.Query["ref"])

	put := f.request(t, stepPutFile)
	assert.Equal(t, doc.Content, decodeContent(t, put.Body))
	assert.Equal(t, doc.Branch, put.Body["branch"])
	assert.Equal(t, "Add RCA for incident 4821", put.Body["message"])
	assert.NotContains(t, put.Body, "sha")

	list := f.request(t, stepListPRs)
	assert.Equal(t, "open", list.Query["state"])
	assert.Equal(t, testOwner+":"+doc.Branch, list.Query["head"])
	assert.Equal(t, testBase, list.Query["base"])

	create := f.request(t, stepCreatePR)
	assert.Equal(t, doc.Title, create.Body["title"])
	assert.Equal(t, doc.Branch, create.Body["head"])
	assert.Equal(t, testBase, create.Body["base"])
	assert.Equal(t, doc.Body, create.Body["body"])

	assert.Contains(t, logs.String(), testPRURL)
	assert.NotContains(t, logs.String(), testToken)
}

func TestPublish_Idempotency(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]response
		wantURL   string
		wantSteps []string
		check     func(t *testing.T, f *fakeGitHub)
	}{
		{
			name: "branch already exists",
			overrides: map[string]response{
				stepCreateRef: {http.StatusUnprocessableEntity, `{"message":"Reference already exists"}`},
			},
			wantURL:   testPRURL,
			wantSteps: []string{stepGetRef, stepCreateRef, stepGetFile, stepPutFile, stepListPRs, stepCreatePR},
		},
		{
			name: "file already exists is updated with its sha",
			overrides: map[string]response{
				stepCreateRef: {http.StatusUnprocessableEntity, `{"message":"Reference already exists"}`},
				stepGetFile:   {http.StatusOK, fmt.Sprintf(`{"type":"file","path":"incidents/2026-09-27-4821.md","sha":%q}`, testFileSHA)},
				stepPutFile:   {http.StatusOK, `{"content":{"sha":"newsha"}}`},
			},
			wantURL:   testPRURL,
			wantSteps: []string{stepGetRef, stepCreateRef, stepGetFile, stepPutFile, stepListPRs, stepCreatePR},
			check: func(t *testing.T, f *fakeGitHub) {
				put := f.request(t, stepPutFile)
				assert.Equal(t, testFileSHA, put.Body["sha"])
				assert.Equal(t, "Update RCA for incident 4821", put.Body["message"])
				assert.Equal(t, "incident-4821", put.Body["branch"])
				assert.Equal(t, testDoc().Content, decodeContent(t, put.Body))
			},
		},
		{
			name: "open pr exists is patched and reused",
			overrides: map[string]response{
				stepListPRs: {http.StatusOK, fmt.Sprintf(`[{"number":3,"html_url":%q}]`, testOldURL)},
			},
			wantURL:   testOldURL,
			wantSteps: []string{stepGetRef, stepCreateRef, stepGetFile, stepPutFile, stepListPRs, stepEditPR},
			check: func(t *testing.T, f *fakeGitHub) {
				edit := f.request(t, stepEditPR)
				assert.Equal(t, "/repos/acme/rca/pulls/3", edit.Query["path"])
				assert.Equal(t, testDoc().Body, edit.Body["body"])
				assert.NotContains(t, edit.Body, "title")
			},
		},
		{
			name: "edit response without url falls back to listed url",
			overrides: map[string]response{
				stepListPRs: {http.StatusOK, fmt.Sprintf(`[{"number":3,"html_url":%q}]`, testOldURL)},
				stepEditPR:  {http.StatusOK, `{"number":3}`},
			},
			wantURL:   testOldURL,
			wantSteps: []string{stepGetRef, stepCreateRef, stepGetFile, stepPutFile, stepListPRs, stepEditPR},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeGitHub(t, tt.overrides)
			p := newTestPublisher(t, f, nil)

			url, err := p.Publish(context.Background(), testDoc())
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, url)
			assert.Equal(t, tt.wantSteps, f.steps())
			if tt.check != nil {
				tt.check(t, f)
			}
		})
	}
}

func TestPublish_Errors(t *testing.T) {
	serverError := response{http.StatusInternalServerError, `{"message":"boom"}`}

	tests := []struct {
		name         string
		overrides    map[string]response
		wantContains string
		wantNotFound bool
		wantLastStep string
	}{
		{
			name:         "missing base branch",
			overrides:    map[string]response{stepGetRef: {http.StatusNotFound, `{"message":"Not Found"}`}},
			wantContains: "github: get base branch main",
			wantNotFound: true,
			wantLastStep: stepGetRef,
		},
		{
			name:         "base ref without sha",
			overrides:    map[string]response{stepGetRef: {http.StatusOK, `{"ref":"refs/heads/main"}`}},
			wantContains: "github: get base branch main: response has no commit sha",
			wantLastStep: stepGetRef,
		},
		{
			name:         "create ref server error",
			overrides:    map[string]response{stepCreateRef: serverError},
			wantContains: "github: create branch incident-4821",
			wantLastStep: stepCreateRef,
		},
		{
			name:         "get file server error",
			overrides:    map[string]response{stepGetFile: serverError},
			wantContains: "github: get file incidents/2026-09-27-4821.md on incident-4821",
			wantLastStep: stepGetFile,
		},
		{
			name:         "get file returns a directory",
			overrides:    map[string]response{stepGetFile: {http.StatusOK, `[{"type":"file","sha":"x"}]`}},
			wantContains: "path is a directory",
			wantLastStep: stepGetFile,
		},
		{
			name:         "create file conflict",
			overrides:    map[string]response{stepPutFile: {http.StatusConflict, `{"message":"conflict"}`}},
			wantContains: "github: create file incidents/2026-09-27-4821.md on incident-4821",
			wantLastStep: stepPutFile,
		},
		{
			name:         "create file server error",
			overrides:    map[string]response{stepPutFile: serverError},
			wantContains: "github: create file",
			wantLastStep: stepPutFile,
		},
		{
			name: "update file conflict",
			overrides: map[string]response{
				stepGetFile: {http.StatusOK, fmt.Sprintf(`{"type":"file","sha":%q}`, testFileSHA)},
				stepPutFile: {http.StatusConflict, `{"message":"sha mismatch"}`},
			},
			wantContains: "github: update file incidents/2026-09-27-4821.md on incident-4821",
			wantLastStep: stepPutFile,
		},
		{
			name:         "list prs server error",
			overrides:    map[string]response{stepListPRs: serverError},
			wantContains: "github: list pull requests for incident-4821",
			wantLastStep: stepListPRs,
		},
		{
			name: "edit pr server error",
			overrides: map[string]response{
				stepListPRs: {http.StatusOK, fmt.Sprintf(`[{"number":3,"html_url":%q}]`, testOldURL)},
				stepEditPR:  serverError,
			},
			wantContains: "github: update pull request #3 for incident-4821",
			wantLastStep: stepEditPR,
		},
		{
			name:         "create pr validation failed",
			overrides:    map[string]response{stepCreatePR: {http.StatusUnprocessableEntity, `{"message":"Validation Failed"}`}},
			wantContains: "github: create pull request for incident-4821",
			wantLastStep: stepCreatePR,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeGitHub(t, tt.overrides)
			p := newTestPublisher(t, f, nil)

			url, err := p.Publish(context.Background(), testDoc())
			require.Error(t, err)
			assert.Empty(t, url)
			assert.Contains(t, err.Error(), tt.wantContains)
			assert.Equal(t, tt.wantNotFound, errors.Is(err, domain.ErrNotFound))
			steps := f.steps()
			require.NotEmpty(t, steps)
			assert.Equal(t, tt.wantLastStep, steps[len(steps)-1])
		})
	}
}

func TestPublish_UnauthorizedNeverLeaksToken(t *testing.T) {
	unauthorized := response{http.StatusUnauthorized, `{"message":"Bad credentials"}`}
	withOpenPR := map[string]response{
		stepListPRs: {http.StatusOK, fmt.Sprintf(`[{"number":3,"html_url":%q}]`, testOldURL)},
	}

	tests := []struct {
		step string
		base map[string]response
	}{
		{step: stepGetRef},
		{step: stepCreateRef},
		{step: stepGetFile},
		{step: stepPutFile},
		{step: stepListPRs},
		{step: stepEditPR, base: withOpenPR},
		{step: stepCreatePR},
	}
	for _, tt := range tests {
		t.Run(tt.step, func(t *testing.T) {
			overrides := map[string]response{}
			for k, v := range tt.base {
				overrides[k] = v
			}
			overrides[tt.step] = unauthorized

			f := newFakeGitHub(t, overrides)
			var logs bytes.Buffer
			p := newTestPublisher(t, f, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

			_, err := p.Publish(context.Background(), testDoc())
			require.Error(t, err)
			var errResp *gh.ErrorResponse
			require.ErrorAs(t, err, &errResp)
			assert.Equal(t, http.StatusUnauthorized, errResp.Response.StatusCode)
			assert.NotContains(t, err.Error(), testToken)
			assert.NotContains(t, logs.String(), testToken)
			steps := f.steps()
			assert.Equal(t, tt.step, steps[len(steps)-1])
		})
	}
}

func TestPublish_ContextCancelled(t *testing.T) {
	f := newFakeGitHub(t, nil)
	p := newTestPublisher(t, f, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	url, err := p.Publish(ctx, testDoc())
	require.Error(t, err)
	assert.Empty(t, url)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "github: get base branch main")
	assert.Empty(t, f.steps(), "no request should reach the server")
}

func TestNew(t *testing.T) {
	valid := Config{Token: testToken, Owner: testOwner, Repo: testRepo, BaseBranch: testBase}

	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{name: "valid without api base url", mutate: func(*Config) {}},
		{name: "valid with trailing slash", mutate: func(c *Config) { c.APIBaseURL = "http://127.0.0.1:1/api/v3/" }},
		{name: "empty token", mutate: func(c *Config) { c.Token = "" }, wantErr: "github: token is required"},
		{name: "empty owner", mutate: func(c *Config) { c.Owner = "" }, wantErr: "github: owner is required"},
		{name: "empty repo", mutate: func(c *Config) { c.Repo = "" }, wantErr: "github: repo is required"},
		{name: "empty base branch", mutate: func(c *Config) { c.BaseBranch = "" }, wantErr: "github: base branch is required"},
		{name: "invalid api base url", mutate: func(c *Config) { c.APIBaseURL = "://bad" }, wantErr: "github: parse api base url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			p, err := New(cfg, nil)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Nil(t, p)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), testToken)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, p)
			assert.True(t, strings.HasSuffix(p.client.BaseURL.String(), "/"))
			if cfg.APIBaseURL != "" {
				assert.Equal(t, cfg.APIBaseURL, p.client.BaseURL.String())
			} else {
				assert.Equal(t, "https://api.github.com/", p.client.BaseURL.String())
			}
		})
	}
}

func TestNew_AddsTrailingSlash(t *testing.T) {
	p, err := New(Config{Token: testToken, Owner: testOwner, Repo: testRepo, BaseBranch: testBase, APIBaseURL: "http://127.0.0.1:1"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:1/", p.client.BaseURL.String())
}
