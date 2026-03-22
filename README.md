# Claude Code Dashboard

![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)
![Realtime](https://img.shields.io/badge/realtime-SSE%20%2F%20WebSocket-0A7EA4)
![Frontend](https://img.shields.io/badge/frontend-vanilla%20JS-F7DF1E?logo=javascript&logoColor=black)
![Backend](https://img.shields.io/badge/backend-Go-00ADD8?logo=go&logoColor=white)

**Claude Code Dashboard** is a real-time multi-agent dashboard built with Go for visualizing Claude Code team and task activity in the browser.

It streams agent status, task progress, and event updates over **SSE** and **WebSocket**, and can read live state directly from local `~/.claude/teams` and `~/.claude/tasks` files.

Built for developers who want a lightweight **Claude Code dashboard**, **multi-agent monitor**, **agent task dashboard**, and **real-time task viewer** without adding heavy frontend or backend dependencies.

## Why this repository

This project is useful if you want to:

- monitor Claude Code multi-agent sessions in real time
- inspect team/task state from local Claude runtime files
- expose the same dashboard state through REST, SSE, and WebSocket
- run a lightweight Go dashboard with a vanilla JS frontend
- extend persistence or event fan-out with Redis, PostgreSQL, or NATS

## Use cases

- **Claude Code team monitoring** — watch active agents, leaders, and assigned work in one dashboard
- **Task flow visualization** — track pending, running, blocked, and completed work across teams
- **Realtime event streaming** — consume the same state updates from browser UI, SSE clients, or WebSocket clients
- **Local development and demos** — run with live Claude local files or fall back to simulation mode
- **Backend experimentation** — use the project as a reference for Go SSE/WebSocket dashboards and multi-agent monitoring

## Highlights

- lightweight Go backend with no heavy framework requirements
- browser UI for agents, tasks, team grouping, and event flow
- realtime transport support through both SSE and WebSocket
- live Claude local runtime integration with simulation fallback
- easy to extend with Redis, PostgreSQL, and NATS adapters

## Screenshots

> Add one or two dashboard screenshots here to improve GitHub click-through and first impression.
>
> Suggested filenames:
>
> - `docs/screenshots/dashboard-overview.png`
> - `docs/screenshots/team-expanded-view.png`

## Features

- Real-time streaming via SSE and handwritten WebSocket (no third-party WS library)
- Claude Code team integration — reads live agent/task state from `~/.claude/` local files
- Pluggable backends: Redis (state snapshots), PostgreSQL (event history), NATS (fan-out)
- Graceful fallback: all backends degrade to in-memory when unconfigured
- Zero-dependency vanilla JS frontend with responsive three-panel layout
- Unified `StreamEnvelope` protocol across all transports
- Bidirectional WebSocket commands with ack/reject/ignore responses
- Simulation mode with auto-advancing task progress for demo/development

## Keywords

Claude Code dashboard, Claude Code monitor, Claude team dashboard, Claude task dashboard, multi-agent dashboard, multi-agent task dashboard, real-time agent dashboard, Go dashboard, SSE dashboard, WebSocket dashboard, team task monitor, agent progress tracker, task flow monitor.

## Quick Start

### What you get

- browser dashboard for Claude Code teams and tasks
- `/api/state` snapshot endpoint
- `/api/events` SSE stream
- `/ws` WebSocket realtime channel
- automatic fallback to simulation mode when Claude local files are unavailable

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

## Why it is easy to integrate

- one shared state model for browser, SSE, and WebSocket consumers
- no heavy frontend framework required
- simple local-file integration with Claude Code runtime data
- optional Redis, PostgreSQL, and NATS support for extension experiments

See [docs/API.md](docs/API.md) for the complete API reference with request/response examples.

## Contributing

1. Fork the repo and create a feature branch
2. Make your changes — `go test ./...` to verify
3. Submit a pull request with a clear description

## License

MIT
