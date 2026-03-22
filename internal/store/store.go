package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/model"
)

type StateStore interface {
	Load(ctx context.Context) (model.DashboardState, error)
	Save(ctx context.Context, state model.DashboardState) error
	HealthCheck(ctx context.Context) error
	Descriptor() model.IntegrationEndpoint
}

type EventStore interface {
	Append(ctx context.Context, event model.Event) error
	Recent(ctx context.Context, limit int) ([]model.Event, error)
	HealthCheck(ctx context.Context) error
	Descriptor() model.IntegrationEndpoint
}

type MemoryStateStore struct {
	mu    sync.RWMutex
	state model.DashboardState
}

type MemoryEventStore struct {
	mu     sync.RWMutex
	events []model.Event
}

type RedisStateStore struct {
	cfg config.RedisConfig
	key string
}

type PostgresEventStore struct {
	cfg        config.PostgresConfig
	schemaOnce sync.Once
	schemaErr  error
}

func NewMemoryStateStore(initial model.DashboardState) *MemoryStateStore {
	return &MemoryStateStore{state: cloneState(initial)}
}

func NewMemoryEventStore(initial []model.Event) *MemoryEventStore {
	cloned := append([]model.Event(nil), initial...)
	return &MemoryEventStore{events: cloned}
}

func NewRedisStateStore(cfg config.RedisConfig) *RedisStateStore {
	return &RedisStateStore{cfg: cfg, key: "dashboard:state"}
}

func NewPostgresEventStore(cfg config.PostgresConfig) *PostgresEventStore {
	return &PostgresEventStore{cfg: cfg}
}

func (s *MemoryStateStore) Load(_ context.Context) (model.DashboardState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state), nil
}

func (s *MemoryStateStore) Save(_ context.Context, state model.DashboardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cloneState(state)
	return nil
}

func (s *MemoryStateStore) HealthCheck(_ context.Context) error {
	return nil
}

func (s *MemoryStateStore) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "memory", Enabled: true, Planned: false, Healthy: true, Notes: "In-memory current state store"}
}

func (s *MemoryEventStore) Append(_ context.Context, event model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append([]model.Event{cloneEvent(event)}, s.events...)
	if len(s.events) > 100 {
		s.events = s.events[:100]
	}
	return nil
}

func (s *MemoryEventStore) Recent(_ context.Context, limit int) ([]model.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}
	out := append([]model.Event(nil), s.events[:limit]...)
	return out, nil
}

func (s *MemoryEventStore) HealthCheck(_ context.Context) error {
	return nil
}

func (s *MemoryEventStore) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "memory", Enabled: true, Planned: false, Healthy: true, Notes: "In-memory event history store"}
}

func (s *RedisStateStore) Load(ctx context.Context) (model.DashboardState, error) {
	conn, err := s.dial(ctx)
	if err != nil {
		return model.DashboardState{}, err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if err := writeRESPArray(conn, "GET", s.key); err != nil {
		return model.DashboardState{}, err
	}
	value, err := readRESPBulkString(reader)
	if err != nil {
		return model.DashboardState{}, err
	}
	if value == "" {
		return model.DashboardState{}, errors.New("redis state missing")
	}
	var state model.DashboardState
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return model.DashboardState{}, err
	}
	return cloneState(state), nil
}

func (s *RedisStateStore) Save(ctx context.Context, state model.DashboardState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if err := writeRESPArray(conn, "SET", s.key, string(payload)); err != nil {
		return err
	}
	return readRESPSimpleOK(reader)
}

func (s *RedisStateStore) HealthCheck(ctx context.Context) error {
	return dialContext(ctx, redisAddress(s.cfg))
}

func (s *RedisStateStore) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "redis", Enabled: s.cfg.Enabled, Planned: true, Endpoint: summarizeRedisEndpoint(s.cfg), Notes: "Stores dashboard snapshot in Redis under dashboard:state"}
}

func (s *PostgresEventStore) Append(ctx context.Context, event model.Event) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	payloadJSON := "{}"
	if event.Payload != nil {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		payloadJSON = string(payload)
	}
	metadataJSON := "{}"
	if event.Metadata != nil {
		metadata, err := json.Marshal(event.Metadata)
		if err != nil {
			return err
		}
		metadataJSON = string(metadata)
	}
	query := fmt.Sprintf(`insert into dashboard_events (
		event_id,
		type,
		level,
		message,
		agent_id,
		task_id,
		created_at,
		payload_json,
		metadata_json
	) values (
		%s,
		%s,
		%s,
		%s,
		%s,
		%s,
		%s,
		%s::jsonb,
		%s::jsonb
	) on conflict (event_id) do nothing`,
		strconv.FormatInt(event.ID, 10),
		quotePostgresLiteral(event.Type),
		quotePostgresLiteral(event.Level),
		quotePostgresLiteral(event.Message),
		quotePostgresLiteral(event.AgentID),
		quotePostgresLiteral(event.TaskID),
		quotePostgresLiteral(event.CreatedAt.UTC().Format(time.RFC3339Nano)),
		quotePostgresLiteral(payloadJSON),
		quotePostgresLiteral(metadataJSON),
	)
	_, err := s.psql(ctx, query)
	return err
}

func (s *PostgresEventStore) Recent(ctx context.Context, limit int) ([]model.Event, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	output, err := s.psql(ctx, fmt.Sprintf(`select
		event_id,
		type,
		level,
		message,
		agent_id,
		task_id,
		to_char(created_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
		coalesce(payload_json::text, '{}'),
		coalesce(metadata_json::text, '{}')
	from dashboard_events
	order by created_at desc, id desc
	limit %d`, limit))
	if err != nil {
		return nil, err
	}
	lines := splitPSQLRows(output)
	out := make([]model.Event, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 9 {
			return nil, fmt.Errorf("unexpected postgres row: %s", line)
		}
		eventID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, err
		}
		createdAt, err := time.Parse(time.RFC3339Nano, parts[6])
		if err != nil {
			return nil, err
		}
		event := model.Event{
			ID:        eventID,
			Type:      parts[1],
			Level:     parts[2],
			Message:   parts[3],
			AgentID:   parts[4],
			TaskID:    parts[5],
			CreatedAt: createdAt,
		}
		if err := unmarshalJSONObject(parts[7], &event.Payload); err != nil {
			return nil, err
		}
		if err := unmarshalJSONObject(parts[8], &event.Metadata); err != nil {
			return nil, err
		}
		out = append(out, cloneEvent(event))
	}
	return out, nil
}

func (s *PostgresEventStore) HealthCheck(ctx context.Context) error {
	_, err := s.psql(ctx, "select 1")
	return err
}

func (s *PostgresEventStore) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "postgresql", Enabled: s.cfg.Enabled, Planned: true, Endpoint: summarizePostgresDSN(s.cfg.DSN), Notes: "Stores dashboard event history in dashboard_events via psql CLI"}
}

func (s *RedisStateStore) dial(ctx context.Context) (net.Conn, error) {
	address := redisAddress(s.cfg)
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("redis endpoint not configured")
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return dialer.DialContext(ctx, "tcp", address)
}

func writeRESPArray(conn net.Conn, parts ...string) error {
	if _, err := fmt.Fprintf(conn, "*%d\r\n", len(parts)); err != nil {
		return err
	}
	for _, part := range parts {
		if _, err := fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(part), part); err != nil {
			return err
		}
	}
	return nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func readRESPBulkString(reader *bufio.Reader) (string, error) {
	line, err := readRESPLine(reader)
	if err != nil {
		return "", err
	}
	if line == "$-1" {
		return "", nil
	}
	if !strings.HasPrefix(line, "$") {
		return "", fmt.Errorf("unexpected redis response: %s", line)
	}
	length, err := strconv.Atoi(strings.TrimPrefix(line, "$"))
	if err != nil {
		return "", err
	}
	payload := make([]byte, length+2)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", err
	}
	return string(payload[:length]), nil
}

func readRESPSimpleOK(reader *bufio.Reader) error {
	line, err := readRESPLine(reader)
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "+OK") {
		return nil
	}
	if strings.HasPrefix(line, "-") {
		return errors.New(strings.TrimPrefix(line, "-"))
	}
	return fmt.Errorf("unexpected redis response: %s", line)
}

func cloneState(state model.DashboardState) model.DashboardState {
	cloned := state
	cloned.Agents = append([]model.Agent(nil), state.Agents...)
	cloned.Tasks = append([]model.Task(nil), state.Tasks...)
	cloned.Events = append([]model.Event(nil), state.Events...)
	return cloned
}

func cloneEvent(event model.Event) model.Event {
	cloned := event
	if event.Payload != nil {
		cloned.Payload = make(map[string]any, len(event.Payload))
		for k, v := range event.Payload {
			cloned.Payload[k] = v
		}
	}
	if event.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(event.Metadata))
		for k, v := range event.Metadata {
			cloned.Metadata[k] = v
		}
	}
	return cloned
}

func dialContext(ctx context.Context, address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("endpoint not configured")
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

func redisAddress(cfg config.RedisConfig) string {
	if cfg.Addr != "" {
		return cfg.Addr
	}
	if cfg.URL == "" {
		return ""
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return ""
}

func postgresAddress(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return ""
		}
		return parsed.Host
	}
	if strings.Contains(dsn, "host=") {
		parts := strings.Fields(dsn)
		host := ""
		port := "5432"
		for _, part := range parts {
			if strings.HasPrefix(part, "host=") {
				host = strings.TrimPrefix(part, "host=")
			}
			if strings.HasPrefix(part, "port=") {
				port = strings.TrimPrefix(part, "port=")
			}
		}
		if host != "" {
			return net.JoinHostPort(host, port)
		}
	}
	return ""
}

func (s *PostgresEventStore) ensureSchema(ctx context.Context) error {
	s.schemaOnce.Do(func() {
		_, s.schemaErr = s.psql(ctx,
			`create table if not exists dashboard_events (
				id bigserial primary key,
				event_id bigint not null,
				type text not null,
				level text not null default '',
				message text not null,
				agent_id text not null default '',
				task_id text not null default '',
				created_at timestamptz not null,
				payload_json jsonb not null default '{}'::jsonb,
				metadata_json jsonb not null default '{}'::jsonb
			);
			create unique index if not exists dashboard_events_event_id_uq on dashboard_events(event_id);
			create index if not exists dashboard_events_created_at_desc_idx on dashboard_events(created_at desc, id desc);`,
		)
	})
	return s.schemaErr
}

func (s *PostgresEventStore) psql(ctx context.Context, args ...string) (string, error) {
	if strings.TrimSpace(s.cfg.DSN) == "" {
		return "", errors.New("postgres endpoint not configured")
	}
	commandArgs := append([]string{"--no-psqlrc", "-X", "-d", s.cfg.DSN, "-v", "ON_ERROR_STOP=1", "-q", "-t", "-A", "-F", "\t", "-c"}, args...)
	cmd := exec.CommandContext(ctx, "psql", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return string(output), nil
}

func splitPSQLRows(raw string) []string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func unmarshalJSONObject[T any](raw string, target *T) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return json.Unmarshal([]byte(trimmed), target)
}

func quotePostgresLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func summarizeRedisEndpoint(cfg config.RedisConfig) string {
	if cfg.Addr != "" {
		return cfg.Addr
	}
	if cfg.URL == "" {
		return ""
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return "configured"
	}
	endpoint := parsed.Host
	if endpoint == "" {
		endpoint = "configured"
	}
	if parsed.Scheme != "" {
		return parsed.Scheme + "://" + endpoint
	}
	return endpoint
}

func SummarizeRedisConfig(cfg config.RedisConfig) string {
	return summarizeRedisEndpoint(cfg)
}

func summarizePostgresDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "configured"
		}
		endpoint := parsed.Host
		if endpoint == "" {
			endpoint = "configured"
		}
		database := strings.TrimPrefix(parsed.Path, "/")
		if database != "" {
			return parsed.Scheme + "://" + endpoint + "/" + database
		}
		if parsed.Scheme != "" {
			return parsed.Scheme + "://" + endpoint
		}
		return endpoint
	}
	if strings.Contains(dsn, "host=") {
		parts := strings.Fields(dsn)
		host := ""
		port := "5432"
		database := ""
		for _, part := range parts {
			switch {
			case strings.HasPrefix(part, "host="):
				host = strings.TrimPrefix(part, "host=")
			case strings.HasPrefix(part, "port="):
				port = strings.TrimPrefix(part, "port=")
			case strings.HasPrefix(part, "dbname="):
				database = strings.TrimPrefix(part, "dbname=")
			}
		}
		if host == "" {
			return "configured"
		}
		endpoint := net.JoinHostPort(host, port)
		if database != "" {
			return endpoint + "/" + database
		}
		return endpoint
	}
	return "configured"
}

func SummarizePostgresConfig(cfg config.PostgresConfig) string {
	return summarizePostgresDSN(cfg.DSN)
}
