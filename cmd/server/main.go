package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"dashboard/internal/bus"
	"dashboard/internal/config"
	"dashboard/internal/httpserver"
	"dashboard/internal/provider"
	"dashboard/internal/store"
)

func main() {
	cfg := config.Load()

	staticDir, err := resolveStaticDir()
	if err != nil {
		log.Fatal(err)
	}

	stateStore := selectStateStore(cfg)
	eventStore := selectEventStore(cfg)
	eventBus := selectEventBus(cfg)

	p := selectProvider(cfg, stateStore, eventStore, eventBus)
	server := httpserver.NewServer(p, staticDir)

	log.Printf("dashboard server listening on %s", cfg.Addr)
	log.Printf("serving web assets from %s", staticDir)
	log.Printf("realtime preferred transport=%s websocket=%t", cfg.Realtime.PreferredTransport, cfg.Realtime.EnableWebSocket)
	if err := http.ListenAndServe(cfg.Addr, server.Routes()); err != nil {
		log.Fatal(err)
	}
}

func selectProvider(cfg config.Config, stateStore store.StateStore, eventStore store.EventStore, eventBus bus.EventBus) provider.StateProvider {
	teamsDir := cfg.Claude.TeamsDir
	tasksDir := cfg.Claude.TasksDir

	if _, err := os.Stat(teamsDir); err != nil {
		log.Printf("claude-runtime: teams dir not found at %s, falling back to memory provider", teamsDir)
		return provider.NewMemoryProvider(stateStore, eventStore, eventBus)
	}

	pollInterval := time.Duration(cfg.Claude.PollInterval) * time.Second
	log.Printf("claude-runtime: scanning all teams in %s (tasks=%s, poll=%s)", teamsDir, tasksDir, pollInterval)
	return provider.NewClaudeRuntimeProvider(teamsDir, tasksDir, pollInterval, eventBus)
}

func selectStateStore(cfg config.Config) store.StateStore {
	if !cfg.Redis.Enabled {
		return nil
	}
	if cfg.Redis.Addr == "" && cfg.Redis.URL == "" {
		log.Printf("redis state store requested but no DASHBOARD_REDIS_ADDR/DASHBOARD_REDIS_URL configured; falling back to in-memory state store")
		return nil
	}
	log.Printf("redis state store requested: endpoint=%s", store.SummarizeRedisConfig(cfg.Redis))
	return store.NewRedisStateStore(cfg.Redis)
}

func selectEventStore(cfg config.Config) store.EventStore {
	if !cfg.Postgres.Enabled {
		return nil
	}
	if cfg.Postgres.DSN == "" {
		log.Printf("postgres event store requested but no DASHBOARD_POSTGRES_DSN configured; falling back to in-memory event store")
		return nil
	}
	log.Printf("postgres event store requested: endpoint=%s", store.SummarizePostgresConfig(cfg.Postgres))
	log.Printf("postgres event store enabled with minimal event append/recent persistence via psql CLI")
	return store.NewPostgresEventStore(cfg.Postgres)
}

func selectEventBus(cfg config.Config) bus.EventBus {
	if !cfg.NATS.Enabled {
		return nil
	}
	if cfg.NATS.URL == "" {
		log.Printf("nats event bus requested but no DASHBOARD_NATS_URL configured; falling back to in-memory bus")
		return nil
	}
	log.Printf("nats event bus requested: endpoint=%s", bus.SummarizeNATSConfig(cfg.NATS))
	log.Printf("nats event bus enabled with minimal core pub/sub implementation")
	return bus.NewNATSBus(cfg.NATS)
}

func resolveStaticDir() (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		for _, candidate := range []string{
			filepath.Join(cwd, "web"),
			filepath.Join(cwd, "..", "..", "web"),
		} {
			if isDir(candidate) {
				return candidate, nil
			}
		}
	}

	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		for _, candidate := range []string{
			filepath.Join(exeDir, "web"),
			filepath.Join(exeDir, "..", "..", "web"),
		} {
			if isDir(candidate) {
				return candidate, nil
			}
		}
	}

	return "", os.ErrNotExist
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
