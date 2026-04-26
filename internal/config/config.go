// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr              string
	Env               string
	RedisURL          string
	SessionSecret     string
	SessionTTL        time.Duration
	BootstrapEmail    string
	BootstrapPassword string

	// TemplatesDir, when set, tells the server to load HTML templates from
	// this directory on disk and re-parse them on every render. That makes
	// edits to web/templates visible on the next request without rebuilding
	// the Go binary. Intended for local dev (docker compose --profile dev);
	// leave empty in production so the embedded FS is used.
	TemplatesDir string

	// Rate limit on the /p/:id hot path. Applied per-IP (c.RealIP()) via a
	// token-bucket in memory. For multi-instance deploys the effective limit
	// is N * RateLimitPerSec. Defaults are tuned to accept large NAT/proxy
	// traffic while still stopping single-IP abuse.
	RateLimitPerSec   float64
	RateLimitBurst    int
	RateLimitDisabled bool

	// Supabase. URL is the project base (https://<ref>.supabase.co). AnonKey
	// is used as the apikey header when calling /auth/v1 endpoints on behalf
	// of end users. ServiceRoleKey is used for privileged operations such as
	// creating the bootstrap super-admin. DBURL is the Postgres transaction
	// pooler URL used by pgx.
	SupabaseURL            string
	SupabaseProjectRef     string
	SupabaseAnonKey        string
	SupabaseServiceRoleKey string
	SupabaseDBURL          string
}

// Load reads configuration from the environment, optionally loading a .env file
// if present in the working directory.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Addr:              getenv("PIXELGO_ADDR", ":8080"),
		Env:               getenv("PIXELGO_ENV", "development"),
		RedisURL:          getenv("PIXELGO_REDIS_URL", "redis://localhost:6379/0"),
		SessionSecret:     os.Getenv("PIXELGO_SESSION_SECRET"),
		BootstrapEmail:    getenv("PIXELGO_BOOTSTRAP_EMAIL", "admin@example.com"),
		BootstrapPassword: os.Getenv("PIXELGO_BOOTSTRAP_PASSWORD"),
		TemplatesDir:      os.Getenv("PIXELGO_TEMPLATES_DIR"),
	}

	ttl, err := time.ParseDuration(getenv("PIXELGO_SESSION_TTL", "168h"))
	if err != nil {
		return nil, err
	}
	cfg.SessionTTL = ttl

	if cfg.SessionSecret == "" {
		if cfg.Env == "production" {
			return nil, errors.New("PIXELGO_SESSION_SECRET is required in production")
		}
		cfg.SessionSecret = "dev-insecure-secret-change-me"
	}

	cfg.RateLimitPerSec = getenvFloat("PIXELGO_RL_PER_SEC", 200)
	cfg.RateLimitBurst = getenvInt("PIXELGO_RL_BURST", 400)
	cfg.RateLimitDisabled = os.Getenv("PIXELGO_RL_DISABLE") == "1"

	cfg.SupabaseURL = os.Getenv("SUPABASE_URL")
	cfg.SupabaseProjectRef = os.Getenv("SUPABASE_PROJECT_REF")
	cfg.SupabaseAnonKey = os.Getenv("SUPABASE_ANON_KEY")
	cfg.SupabaseServiceRoleKey = os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	cfg.SupabaseDBURL = os.Getenv("SUPABASE_DB_URL")
	if cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" || cfg.SupabaseDBURL == "" {
		return nil, errors.New("SUPABASE_URL, SUPABASE_ANON_KEY, and SUPABASE_DB_URL are required")
	}

	return cfg, nil
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
