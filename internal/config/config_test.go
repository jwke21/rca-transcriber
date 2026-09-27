package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validEnv() map[string]string {
	return map[string]string{
		"PUBLIC_BASE_URL":       "https://example.ngrok-free.app",
		"DATABASE_URL":          "postgres://rca:rca@localhost:5432/rca?sslmode=disable",
		"STREAM_SIGNING_SECRET": strings.Repeat("a", 32),
		"TWILIO_ACCOUNT_SID":    "ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"TWILIO_AUTH_TOKEN":     "twilio-token",
		"DEEPGRAM_API_KEY":      "deepgram-key",
		"DEEPGRAM_MODEL":        "nova-3",
		"GEMINI_API_KEY":        "gemini-key",
		"GEMINI_MODEL":          "gemini-2.5-flash",
		"GITHUB_TOKEN":          "github_pat_xxx",
		"GITHUB_OWNER":          "owner",
		"GITHUB_REPO":           "repo",
	}
}

func getenvFrom(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func writeEnvFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestLoad_AllRequiredPresent_DefaultsApplied(t *testing.T) {
	cfg, err := load("/nonexistent/.env", getenvFrom(validEnv()))
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "main", cfg.GitHubBaseBranch)
	assert.Equal(t, "https://example.ngrok-free.app", cfg.PublicBaseURL)
}

func TestLoad_MissingRequiredVariables_AllNamedInError(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	delete(env, "GITHUB_TOKEN")

	_, err := load("/nonexistent/.env", getenvFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL is required")
	assert.Contains(t, err.Error(), "GITHUB_TOKEN is required")
}

func TestLoad_PublicBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid https", "https://example.ngrok-free.app", false},
		{"rejects http", "http://example.ngrok-free.app", true},
		{"rejects relative", "/twilio/voice", true},
		{"rejects trailing slash", "https://example.ngrok-free.app/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["PUBLIC_BASE_URL"] = tt.url
			_, err := load("/nonexistent/.env", getenvFrom(env))
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "PUBLIC_BASE_URL")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoad_StreamSigningSecretTooShort(t *testing.T) {
	env := validEnv()
	env["STREAM_SIGNING_SECRET"] = strings.Repeat("a", 31)
	_, err := load("/nonexistent/.env", getenvFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STREAM_SIGNING_SECRET must be at least 32 characters")
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	env := validEnv()
	env["LOG_LEVEL"] = "verbose"
	_, err := load("/nonexistent/.env", getenvFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOG_LEVEL")
}

func TestLoad_InvalidPort(t *testing.T) {
	env := validEnv()
	env["PORT"] = "not-a-number"
	_, err := load("/nonexistent/.env", getenvFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PORT")
}

func TestLoad_EnvFileLoadedAndOverridden(t *testing.T) {
	fileEnv := validEnv()
	fileEnv["DEEPGRAM_MODEL"] = "from-file"
	var sb strings.Builder
	for k, v := range fileEnv {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	path := writeEnvFile(t, sb.String())

	// The real process environment overrides the .env file.
	processEnv := map[string]string{"DEEPGRAM_MODEL": "from-process-env"}

	cfg, err := load(path, getenvFrom(processEnv))
	require.NoError(t, err)
	assert.Equal(t, "from-process-env", cfg.DeepgramModel)
	assert.Equal(t, "postgres://rca:rca@localhost:5432/rca?sslmode=disable", cfg.DatabaseURL)
}

func TestConfig_Redacted_MasksEverySecret(t *testing.T) {
	cfg, err := load("/nonexistent/.env", getenvFrom(validEnv()))
	require.NoError(t, err)

	redacted := cfg.Redacted()
	for _, key := range []string{"STREAM_SIGNING_SECRET", "TWILIO_AUTH_TOKEN", "DEEPGRAM_API_KEY", "GEMINI_API_KEY", "GITHUB_TOKEN"} {
		assert.Equal(t, "***", redacted[key], key)
	}
	assert.NotContains(t, redacted["DATABASE_URL"], "rca:rca@")
	assert.Equal(t, "owner", redacted["GITHUB_OWNER"])
}
