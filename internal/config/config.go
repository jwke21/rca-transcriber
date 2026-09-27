// Package config loads and validates the service's configuration from
// environment variables (and an optional .env file).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds every setting the service needs. Never log it directly; use
// Redacted for logging.
type Config struct {
	Port                int
	PublicBaseURL       string
	DatabaseURL         string
	StreamSigningSecret string
	LogLevel            string

	TwilioAccountSID string
	TwilioAuthToken  string

	DeepgramAPIKey string
	DeepgramModel  string

	GeminiAPIKey string
	GeminiModel  string

	GitHubToken      string
	GitHubOwner      string
	GitHubRepo       string
	GitHubBaseBranch string
}

var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// Load loads .env (if present; real environment variables win), applies
// defaults, validates every setting and returns all problems joined into one
// error.
func Load() (Config, error) {
	return load(".env", os.LookupEnv)
}

// load is the testable core of Load: it never touches the real process
// environment or filesystem beyond the given path and getenv function.
func load(envFilePath string, getenv func(string) (string, bool)) (Config, error) {
	fileValues := map[string]string{}
	if _, err := os.Stat(envFilePath); err == nil {
		values, err := godotenv.Read(envFilePath)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", envFilePath, err)
		}
		fileValues = values
	}

	lookup := func(key string) (string, bool) {
		if v, ok := getenv(key); ok {
			return v, true
		}
		if v, ok := fileValues[key]; ok {
			return v, true
		}
		return "", false
	}

	var errs []error
	req := func(key string) string {
		v, ok := lookup(key)
		if !ok || strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
			return ""
		}
		return v
	}
	opt := func(key, def string) string {
		v, ok := lookup(key)
		if !ok || strings.TrimSpace(v) == "" {
			return def
		}
		return v
	}

	cfg := Config{}

	portStr := opt("PORT", "8080")
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil {
		errs = append(errs, fmt.Errorf("PORT must be numeric: %q", portStr))
	}
	cfg.Port = port

	cfg.PublicBaseURL = req("PUBLIC_BASE_URL")
	if cfg.PublicBaseURL != "" {
		if err := validatePublicBaseURL(cfg.PublicBaseURL); err != nil {
			errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL: %w", err))
		}
	}

	cfg.DatabaseURL = req("DATABASE_URL")

	cfg.StreamSigningSecret = req("STREAM_SIGNING_SECRET")
	if cfg.StreamSigningSecret != "" && len(cfg.StreamSigningSecret) < 32 {
		errs = append(errs, errors.New("STREAM_SIGNING_SECRET must be at least 32 characters"))
	}

	cfg.LogLevel = strings.ToLower(opt("LOG_LEVEL", "info"))
	if !validLogLevels[cfg.LogLevel] {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", cfg.LogLevel))
	}

	cfg.TwilioAccountSID = req("TWILIO_ACCOUNT_SID")
	if cfg.TwilioAccountSID != "" && !strings.HasPrefix(cfg.TwilioAccountSID, "AC") {
		errs = append(errs, errors.New("TWILIO_ACCOUNT_SID must start with AC"))
	}
	cfg.TwilioAuthToken = req("TWILIO_AUTH_TOKEN")

	cfg.DeepgramAPIKey = req("DEEPGRAM_API_KEY")
	cfg.DeepgramModel = req("DEEPGRAM_MODEL")

	cfg.GeminiAPIKey = req("GEMINI_API_KEY")
	cfg.GeminiModel = req("GEMINI_MODEL")

	cfg.GitHubToken = req("GITHUB_TOKEN")
	cfg.GitHubOwner = req("GITHUB_OWNER")
	cfg.GitHubRepo = req("GITHUB_REPO")
	cfg.GitHubBaseBranch = opt("GITHUB_BASE_BRANCH", "main")

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func validatePublicBaseURL(raw string) error {
	if strings.HasSuffix(raw, "/") {
		return fmt.Errorf("must not have a trailing slash: %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must be an absolute https:// URL, got %q", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("must be an absolute https:// URL, got %q", raw)
	}
	return nil
}

// Redacted returns the config as a map with every secret masked, safe to log.
func (c Config) Redacted() map[string]string {
	return map[string]string{
		"PORT":                  strconv.Itoa(c.Port),
		"PUBLIC_BASE_URL":       c.PublicBaseURL,
		"DATABASE_URL":          redactURL(c.DatabaseURL),
		"STREAM_SIGNING_SECRET": redactSecret(c.StreamSigningSecret),
		"LOG_LEVEL":             c.LogLevel,
		"TWILIO_ACCOUNT_SID":    c.TwilioAccountSID,
		"TWILIO_AUTH_TOKEN":     redactSecret(c.TwilioAuthToken),
		"DEEPGRAM_API_KEY":      redactSecret(c.DeepgramAPIKey),
		"DEEPGRAM_MODEL":        c.DeepgramModel,
		"GEMINI_API_KEY":        redactSecret(c.GeminiAPIKey),
		"GEMINI_MODEL":          c.GeminiModel,
		"GITHUB_TOKEN":          redactSecret(c.GitHubToken),
		"GITHUB_OWNER":          c.GitHubOwner,
		"GITHUB_REPO":           c.GitHubRepo,
		"GITHUB_BASE_BRANCH":    c.GitHubBaseBranch,
	}
}

func redactSecret(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// redactURL masks the userinfo (username/password) portion of a URL, if any,
// while keeping the rest visible for debugging.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.UserPassword("***", "***")
	return u.String()
}
