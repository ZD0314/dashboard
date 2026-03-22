# Go Real-Time Multi-Agent Dashboard

**多智能体实时监控仪表盘** — A lightweight Go server that streams simulated (or real) multi-agent task progress to a browser UI over SSE and WebSocket.

Built to visualize [Claude Code](https://claude.ai/code) multi-agent team sessions in real time, with pluggable persistence and transport backends.

## Features

- Real-time streaming via SSE and handwritten WebSocket (no third-party WS library)
- Claude Code team integration — reads live agent/task state from `~/.claude/` local files
- Pluggable backends: Redis (state snapshots), PostgreSQL (event history), NATS (fan-out)
- Graceful fallback: all backends degrade to in-memory when unconfigured
- Zero-dependency vanilla JS frontend with responsive three-panel layout
- Unified `StreamEnvelope` protocol across all transports
- Bidirectional WebSocket commands with ack/reject/ignore responses
- Simulation mode with auto-advancing task progress for demo/development

## Quick Start

### Prerequisites

- Go 1.21+

### Run

```bash
git clone <repo-url> && cd dashboard
go run ./cmd/server
```

Open [http://localhost:8080](http://localhost:8080) in your browser.

### Build

```bash
go build -o dashboard ./cmd/server
./dashboard
```

## Configuration

All configuration is via environment variables. Everything works out of the box with defaults.

| Variable | Default | Description |
|----------|---------|-------------|
| `ADDR` | `:8080` | HTTP listen address |
| `DASHBOARD_REALTIME_TRANSPORT` | `sse` | Preferred realtime transport |
| `DASHBOARD_ENABLE_WS` | `true` | Enable WebSocket endpoint |
| `DASHBOARD_REDIS_ENABLED` | `false` | Enable Redis state store |
| `DASHBOARD_REDIS_ADDR` | — | Redis address (host:port) |
| `DASHBOARD_REDIS_URL` | — | Redis URL |
| `DASHBOARD_POSTGRES_ENABLED` | `false` | Enable PostgreSQL event store |
| `DASHBOARD_POSTGRES_DSN` | — | PostgreSQL connection string |
| `DASHBOARD_NATS_ENABLED` | `false` | Enable NATS event bus |
| `DASHBOARD_NATS_URL` | — | NATS server URL |
| `DASHBOARD_CLAUDE_TEAMS_DIR` | `~/.claude/teams` | Claude teams config directory |
| `DASHBOARD_CLAUDE_TASKS_DIR` | `~/.claude/tasks` | Claude tasks data directory |
| `DASHBOARD_CLAUDE_POLL_INTERVAL` | `2` (seconds) | File polling interval for Claude runtime |

## Architecture

The server follows a layered architecture: a shared model defines the data contract, a provider layer handles state orchestration (either from Claude local files or an in-memory simulator), pluggable store/bus adapters handle persistence and fan-out, and an HTTP layer exposes everything to the browser via REST, SSE, and WebSocket.

- **model** — Canonical schema: `DashboardState`, `Agent`, `Task`, `Event`, `StreamEnvelope`, WebSocket command/ack types
- **provider** — `ClaudeRuntimeProvider` (reads real Claude team/task files) or `MemoryProvider` (simulation mode with periodic progress ticks)
- **store** — `StateStore` (in-memory / Redis) and `EventStore` (in-memory / PostgreSQL). Redis and PostgreSQL adapters use raw TCP/CLI — no external Go client libraries
- **bus** — `EventBus` for realtime fan-out (in-memory / NATS core pub/sub)
- **httpserver** — Routes, SSE streaming, handwritten WebSocket upgrade and frame handling
- **config** — Environment-variable-driven configuration with sensible defaults

## Project Structure

```
dashboard/
├── cmd/server/
│   └── main.go              # Entrypoint, wiring, backend selection
├── internal/
│   ├── model/model.go       # Shared data types and protocol constants
│   ├── config/config.go     # Environment-driven configuration
│   ├── provider/
│   │   ├── claude_runtime.go # Real mode: reads ~/.claude/ files
│   │   └── memory.go        # Simulation mode: auto-advancing demo
│   ├── store/
│   │   ├── store.go         # StateStore + EventStore interfaces and adapters
│   │   └── store_test.go
│   ├── bus/
│   │   ├── bus.go           # EventBus interface and adapters
│   │   └── bus_test.go
│   └── httpserver/
│       └── server.go        # HTTP routes, SSE, WebSocket
├── web/
│   ├── index.html           # Dashboard UI
│   ├── app.js               # Frontend logic (vanilla JS)
│   └── styles.css           # Responsive styles
├── docs/
│   ├── API.md               # Full API reference
│   └── FRONTEND.md          # Frontend architecture docs
├── ARCHITECTURE.md           # System architecture details
└── CLAUDE.md                 # Claude Code development guide
```

## API Overview

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/state` | GET | Returns the full `DashboardState` JSON snapshot |
| `/api/events` | GET (SSE) | Streams `snapshot`, `update`, and `heartbeat` events as `StreamEnvelope` |
| `/ws` | WebSocket | Bidirectional: server pushes same envelopes, client sends commands and receives acks |

See [docs/API.md](docs/API.md) for the complete API reference with request/response examples.

## Contributing

1. Fork the repo and create a feature branch
2. Make your changes — `go test ./...` to verify
3. Submit a pull request with a clear description

## License

MIT
