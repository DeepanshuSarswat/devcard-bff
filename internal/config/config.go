// Package config loads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable of the service. Zero values are never used
// directly: Load always fills in defaults.
type Config struct {
	Port            string
	GitHubToken     string
	GitHubBaseURL   string
	CacheTTL        time.Duration
	AllowedOrigin   string
	UpstreamTimeout time.Duration
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
}

// Defaults used when the corresponding env var is unset or empty.
const (
	DefaultPort            = "8080"
	DefaultGitHubBaseURL   = "https://api.github.com"
	DefaultCacheTTL        = 5 * time.Minute
	DefaultAllowedOrigin   = "http://localhost:5173"
	DefaultUpstreamTimeout = 5 * time.Second
	DefaultShutdownTimeout = 15 * time.Second
)

// Load reads configuration using getenv (normally os.Getenv). Taking the
// lookup function as a parameter keeps Load pure and trivially testable.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:          valueOr(getenv("PORT"), DefaultPort),
		GitHubToken:   strings.TrimSpace(getenv("GITHUB_TOKEN")),
		GitHubBaseURL: strings.TrimRight(valueOr(getenv("GITHUB_API_URL"), DefaultGitHubBaseURL), "/"),
		AllowedOrigin: valueOr(getenv("ALLOWED_ORIGIN"), DefaultAllowedOrigin),
	}

	var errs []error
	var err error

	if _, err = strconv.ParseUint(cfg.Port, 10, 16); err != nil {
		errs = append(errs, fmt.Errorf("PORT: %q is not a valid port", cfg.Port))
	}
	if cfg.CacheTTL, err = durationOr(getenv("CACHE_TTL"), DefaultCacheTTL); err != nil {
		errs = append(errs, fmt.Errorf("CACHE_TTL: %w", err))
	}
	if cfg.UpstreamTimeout, err = durationOr(getenv("UPSTREAM_TIMEOUT"), DefaultUpstreamTimeout); err != nil {
		errs = append(errs, fmt.Errorf("UPSTREAM_TIMEOUT: %w", err))
	}
	if cfg.ShutdownTimeout, err = durationOr(getenv("SHUTDOWN_TIMEOUT"), DefaultShutdownTimeout); err != nil {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
	}
	if lvl := getenv("LOG_LEVEL"); lvl != "" {
		if err = cfg.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}

	// errors.Join reports every bad variable at once instead of making the
	// operator fix them one restart at a time. It returns nil for an empty slice.
	return cfg, errors.Join(errs...)
}

func valueOr(v, def string) string {
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

func durationOr(v string, def time.Duration) (time.Duration, error) {
	if v = strings.TrimSpace(v); v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", d)
	}
	return d, nil
}
