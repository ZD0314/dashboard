package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr      string
	Realtime  RealtimeConfig
	Redis     RedisConfig
	Postgres  PostgresConfig
	NATS      NATSConfig
	Claude    ClaudeConfig
}

type ClaudeConfig struct {
	TeamsDir     string // ~/.claude/teams
	TasksDir     string // ~/.claude/tasks
	PollInterval int    // seconds, default 2
}

type RealtimeConfig struct {
	PreferredTransport string
	EnableWebSocket    bool
}

type RedisConfig struct {
	Enabled bool
	Addr    string
	URL     string
}

type PostgresConfig struct {
	Enabled bool
	DSN     string
}

type NATSConfig struct {
	Enabled bool
	URL     string
}

func Load() Config {
	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = ":8080"
	}

	preferred := strings.ToLower(strings.TrimSpace(os.Getenv("DASHBOARD_REALTIME_TRANSPORT")))
	if preferred == "" {
		preferred = "sse"
	}

	return Config{
		Addr: addr,
		Realtime: RealtimeConfig{
			PreferredTransport: preferred,
			EnableWebSocket:    envBool("DASHBOARD_ENABLE_WS", true),
		},
		Redis: RedisConfig{
			Enabled: envBool("DASHBOARD_REDIS_ENABLED", false),
			Addr:    strings.TrimSpace(os.Getenv("DASHBOARD_REDIS_ADDR")),
			URL:     strings.TrimSpace(os.Getenv("DASHBOARD_REDIS_URL")),
		},
		Postgres: PostgresConfig{
			Enabled: envBool("DASHBOARD_POSTGRES_ENABLED", false),
			DSN:     strings.TrimSpace(os.Getenv("DASHBOARD_POSTGRES_DSN")),
		},
		NATS: NATSConfig{
			Enabled: envBool("DASHBOARD_NATS_ENABLED", false),
			URL:     strings.TrimSpace(os.Getenv("DASHBOARD_NATS_URL")),
		},
		Claude: loadClaudeConfig(),
	}
}

func loadClaudeConfig() ClaudeConfig {
	home, _ := os.UserHomeDir()

	teamsDir := strings.TrimSpace(os.Getenv("DASHBOARD_CLAUDE_TEAMS_DIR"))
	if teamsDir == "" {
		teamsDir = filepath.Join(home, ".claude", "teams")
	}
	tasksDir := strings.TrimSpace(os.Getenv("DASHBOARD_CLAUDE_TASKS_DIR"))
	if tasksDir == "" {
		tasksDir = filepath.Join(home, ".claude", "tasks")
	}

	pollInterval := 2
	if v := strings.TrimSpace(os.Getenv("DASHBOARD_CLAUDE_POLL_INTERVAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pollInterval = n
		}
	}

	return ClaudeConfig{
		TeamsDir:     teamsDir,
		TasksDir:     tasksDir,
		PollInterval: pollInterval,
	}
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
