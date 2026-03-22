# Security Audit Report

**Project:** dashboard (Go + vanilla JS)
**Date:** 2026-03-22
**Auditor:** security-scanner (automated)

---

## Summary

| Severity | Count |
|----------|-------|
| CRITICAL | 1     |
| HIGH     | 3     |
| MEDIUM   | 4     |
| LOW      | 3     |

---

## CRITICAL

### SEC-01: SQL Injection via `psql` CLI Shell-Out

**File:** `internal/store/store.go:467-482`
**Severity:** CRITICAL

The `PostgresEventStore.psql()` method passes SQL queries directly to the `psql` command-line tool via `exec.CommandContext`. While the `Append()` method uses `quotePostgresLiteral()` to escape single quotes in values, this escaping is insufficient to prevent all injection vectors:

1. `quotePostgresLiteral()` (line 505-507) only replaces `'` with `''`. It does not handle backslash escapes (`\'`), null bytes, or PostgreSQL dollar-quoting (`$$...$$`). If `standard_conforming_strings` is `off` on the target server, a backslash-based escape can break out of the literal.
2. The `Recent()` method (line 236) interpolates the `limit` parameter directly into the SQL string via `fmt.Sprintf`. While `limit` is an `int` and not directly user-controlled in the current code, this pattern is fragile and could become exploitable if the call chain changes.
3. The entire DSN (`s.cfg.DSN`) is passed as a `-d` argument to `psql` (line 471). If the DSN is sourced from an untrusted environment variable, a crafted DSN could include shell metacharacters or `psql` connection parameters that alter behavior (e.g., `sslrootcert`, `options`).

**Impact:** If PostgreSQL integration is enabled and any event field (type, message, agentId, taskId) can be influenced by an external source, an attacker could execute arbitrary SQL on the database.

**Recommendation:** Replace the `psql` CLI shell-out with a proper Go database driver (`lib/pq` or `pgx`). If the CLI approach must be kept, use parameterized queries via `psql` variables or pipe input through stdin rather than embedding values in the command string.

---

## HIGH

### SEC-02: No WebSocket Origin Validation

**File:** `internal/httpserver/server.go:115-144`
**Severity:** HIGH

The `handleWebSocket()` method performs the WebSocket upgrade handshake without checking the `Origin` header. This allows any website to open a WebSocket connection to the dashboard server from a user's browser (Cross-Site WebSocket Hijacking / CSWSH).

An attacker-controlled page could:
- Read the full dashboard state (agents, tasks, events) in real time
- Send commands through the WebSocket (ping, pause_agent, resume_agent, reassign_task)

**Impact:** Information disclosure and unauthorized command execution via cross-origin WebSocket connections.

**Recommendation:** Validate the `Origin` header against an allowlist of expected origins before completing the upgrade handshake.

---

### SEC-03: Missing Security Response Headers

**File:** `internal/httpserver/server.go:31-43`
**Severity:** HIGH

The HTTP server does not set any security-related response headers. Missing headers include:

- `X-Content-Type-Options: nosniff` — allows MIME-type sniffing attacks
- `X-Frame-Options: DENY` (or `SAMEORIGIN`) — allows clickjacking via iframe embedding
- `Content-Security-Policy` — no CSP means no defense-in-depth against XSS
- `Strict-Transport-Security` — no HSTS if deployed behind TLS
- `X-XSS-Protection` — legacy but still useful for older browsers
- `Referrer-Policy` — no control over referrer leakage

**Impact:** The dashboard is vulnerable to clickjacking, MIME confusion, and lacks defense-in-depth against XSS.

**Recommendation:** Add a middleware that sets these headers on all responses.

---

### SEC-04: Static File Serving May Allow Path Traversal

**File:** `internal/httpserver/server.go:33,37`
**Severity:** HIGH

Two routes serve static files using `http.FileServer(http.Dir(s.staticDir))`:

```go
mux.Handle("/app.js", http.FileServer(http.Dir(s.staticDir)))
mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
```

Go's `http.FileServer` does sanitize paths and block `..` traversal by default. However, the `/static/` route exposes the entire `staticDir` directory tree. If `staticDir` is resolved to a parent directory (e.g., via symlinks or the `../../web` fallback in `resolveStaticDir()`), files outside the intended web root could be served.

Additionally, `resolveStaticDir()` in `cmd/server/main.go:92-119` walks up to `../../web` from both `cwd` and the executable directory. If the binary is placed in an unexpected location, this could resolve to an unintended directory.

**Impact:** Potential exposure of files outside the intended web root depending on deployment layout.

**Recommendation:** Use `filepath.Abs()` + `filepath.EvalSymlinks()` on the resolved static directory and verify it is within the expected project boundary. Consider restricting `/static/` to specific file extensions.

---

## MEDIUM

### SEC-05: No CORS Headers on API Endpoints

**File:** `internal/httpserver/server.go:58-61`
**Severity:** MEDIUM

The `/api/state` and `/api/events` endpoints do not set any CORS headers. While the absence of `Access-Control-Allow-Origin` means browsers will block cross-origin reads by default (which is good), there is no explicit CORS policy. If CORS headers are added later without care, the API could become open to cross-origin abuse.

The SSE endpoint (`/api/events`) is particularly notable because `EventSource` does not enforce same-origin by default in all browsers — it sends cookies cross-origin.

**Impact:** Without explicit CORS denial, future changes could inadvertently open the API to cross-origin access. SSE connections may leak state cross-origin if cookies are used for auth.

**Recommendation:** Explicitly set `Access-Control-Allow-Origin` to the expected origin (or omit it to rely on browser defaults) and add CORS preflight handling if the API will be accessed cross-origin.

---

### SEC-06: WebSocket Frame Size Not Limited

**File:** `internal/httpserver/server.go:300-338`
**Severity:** MEDIUM

The `readWebSocketFrame()` function reads the frame length from the client and allocates a buffer of that size (line 328: `payload := make([]byte, length)`). A malicious client can send a frame header claiming a payload of several gigabytes, causing the server to attempt a massive memory allocation.

**Impact:** Denial of service via memory exhaustion from a single WebSocket connection.

**Recommendation:** Enforce a maximum frame size (e.g., 1 MB) and reject frames that exceed it.

---

### SEC-07: Sensitive Configuration Logged in Plaintext

**File:** `cmd/server/main.go:32-34,62,74-75,87-88`
**Severity:** MEDIUM

The server logs configuration details at startup including Redis endpoints, PostgreSQL DSN summaries, and NATS endpoints. While the `summarize*` functions attempt to strip credentials, the PostgreSQL DSN could still contain username/password in the URL path or query parameters that are not fully redacted by `summarizePostgresDSN()`.

Additionally, `store.go:534-581` (`summarizePostgresDSN`) preserves the scheme, host, and database name but does not explicitly strip `user:password@` from URL-style DSNs.

**Impact:** Database credentials could be exposed in log output.

**Recommendation:** Ensure all `summarize*` functions explicitly strip userinfo from URLs before logging. Use `parsed.Redacted()` or manually clear `parsed.User` before formatting.

---

### SEC-08: No Rate Limiting or Authentication

**File:** `internal/httpserver/server.go` (all routes)
**Severity:** MEDIUM

The entire HTTP server has no authentication or authorization mechanism. All endpoints (`/api/state`, `/api/events`, `/ws`, `/static/`) are publicly accessible. There is also no rate limiting, meaning:

- Any client can open unlimited SSE/WebSocket connections
- Any client can poll `/api/state` at arbitrary frequency
- WebSocket commands (pause_agent, resume_agent, reassign_task) can be sent by anyone

**Impact:** Unauthorized access to dashboard state and command execution. Resource exhaustion via connection flooding.

**Recommendation:** Add authentication (at minimum, a shared secret or token) and rate limiting. For internal-only deployments, bind to `127.0.0.1` instead of `0.0.0.0`.

---

## LOW

### SEC-09: `http.ListenAndServe` Binds to All Interfaces by Default

**File:** `cmd/server/main.go:35`
**Severity:** LOW

The default bind address is `:8080` (all interfaces). Combined with the lack of authentication (SEC-08), this exposes the dashboard to the entire network.

**Impact:** Network-adjacent attackers can access the dashboard.

**Recommendation:** Default to `127.0.0.1:8080` for local-only access. Require explicit configuration to bind to all interfaces.

---

### SEC-10: No TLS Configuration

**File:** `cmd/server/main.go:35`
**Severity:** LOW

The server uses `http.ListenAndServe` (plaintext HTTP). All data including dashboard state, events, and WebSocket commands are transmitted unencrypted.

**Impact:** Network eavesdropping can capture all dashboard data in transit.

**Recommendation:** Support `http.ListenAndServeTLS` or deploy behind a TLS-terminating reverse proxy.

---

### SEC-11: Frontend `innerHTML` Usage is Properly Escaped (Informational)

**File:** `web/app.js`
**Severity:** LOW (Informational)

The frontend uses `innerHTML` in several places (`renderTask`, `renderAgents`, `renderEvents`, `renderChips`). All dynamic values are passed through `escapeHtml()` (line 640-647) which covers the five critical characters (`&`, `<`, `>`, `"`, `'`).

However, two locations set `innerHTML` with static strings that do not go through `escapeHtml()`:
- Line 244: `elements.agentList.innerHTML = '暂无 agent 数据';` (safe, static)
- Line 46: `elements.taskCard.innerHTML = \`...\${escapeHtml(error.message)}...\`` (properly escaped)

The `escapeHtml` function is comprehensive for HTML context injection. No XSS vulnerability was found in the current code. The `textContent` assignments in `buildAgentItem` and `buildEventItem` are inherently safe.

**Note:** The `escapeHtml` function does not protect against injection in URL or JavaScript contexts, but no such usage exists in the current code.

---

## Appendix: Files Reviewed

| File | Lines |
|------|-------|
| `cmd/server/main.go` | 128 |
| `internal/httpserver/server.go` | 339 |
| `internal/provider/memory.go` | 472 |
| `internal/provider/claude_runtime.go` | 542 |
| `internal/store/store.go` | 586 |
| `internal/bus/bus.go` | 310 |
| `internal/config/config.go` | 113 |
| `internal/model/model.go` | 118 |
| `web/app.js` | 648 |
| `web/index.html` | 97 |
