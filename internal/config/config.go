package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	BaseURL      string
	DBPath       string
	Vec0Path     string
	EmbedDim     int
	TLSDomain    string
	ListenAddr   string
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
	MagicTTL     time.Duration
	NodeStaleAge time.Duration
	MCPEndpoint  string
}

func Load() *Config {
	return &Config{
		BaseURL:      env("HB_BASE_URL", "http://localhost:8080"),
		DBPath:       env("HB_DB_PATH", "data/hirebridge.db"),
		Vec0Path:     env("HB_VEC0_PATH", "/app/ext/vec0.so"),
		EmbedDim:     envInt("HB_EMBED_DIM", 384),
		TLSDomain:    env("HB_TLS_DOMAIN", ""),
		ListenAddr:   env("HB_LISTEN", ":8080"),
		ResendAPIKey: env("HB_RESEND_API_KEY", ""),
		SMTPHost:     env("HB_SMTP_HOST", ""),
		SMTPPort:     envInt("HB_SMTP_PORT", 587),
		SMTPUser:     env("HB_SMTP_USER", ""),
		SMTPPass:     env("HB_SMTP_PASS", ""),
		SMTPFrom:     env("HB_SMTP_FROM", "hirebridge@localhost"),
		MagicTTL:     envDuration("HB_MAGIC_TTL", 15*time.Minute),
		NodeStaleAge: envDuration("HB_NODE_PING_STALE", 90*time.Second),
		MCPEndpoint:  env("HB_MCP_ENDPOINT", "/mcp"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
