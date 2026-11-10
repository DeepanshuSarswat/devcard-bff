package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// env builds a getenv func from a map, so tests never touch the real process env.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Port:            DefaultPort,
		GitHubBaseURL:   DefaultGitHubBaseURL,
		CacheTTL:        DefaultCacheTTL,
		AllowedOrigin:   DefaultAllowedOrigin,
		UpstreamTimeout: DefaultUpstreamTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
		LogLevel:        slog.LevelInfo,
	}
	if cfg != want {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PORT":             "9090",
		"GITHUB_TOKEN":     "  tok  ",
		"GITHUB_API_URL":   "http://example.test/",
		"CACHE_TTL":        "30s",
		"ALLOWED_ORIGIN":   "https://app.example.com",
		"UPSTREAM_TIMEOUT": "2s",
		"SHUTDOWN_TIMEOUT": "1m",
		"LOG_LEVEL":        "debug",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Port:            "9090",
		GitHubToken:     "tok",
		GitHubBaseURL:   "http://example.test",
		CacheTTL:        30 * time.Second,
		AllowedOrigin:   "https://app.example.com",
		UpstreamTimeout: 2 * time.Second,
		ShutdownTimeout: time.Minute,
		LogLevel:        slog.LevelDebug,
	}
	if cfg != want {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
	}{
		{"bad port", map[string]string{"PORT": "http"}, []string{"PORT"}},
		{"port out of range", map[string]string{"PORT": "70000"}, []string{"PORT"}},
		{"bad ttl", map[string]string{"CACHE_TTL": "five"}, []string{"CACHE_TTL"}},
		{"negative ttl", map[string]string{"CACHE_TTL": "-1s"}, []string{"CACHE_TTL", "positive"}},
		{"bad log level", map[string]string{"LOG_LEVEL": "loud"}, []string{"LOG_LEVEL"}},
		{
			"reports all errors",
			map[string]string{"UPSTREAM_TIMEOUT": "x", "SHUTDOWN_TIMEOUT": "0s"},
			[]string{"UPSTREAM_TIMEOUT", "SHUTDOWN_TIMEOUT"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}
