package store

import (
	"testing"

	"dashboard/internal/config"
)

func TestPostgresAddress(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{name: "empty", dsn: "", want: ""},
		{name: "url", dsn: "postgres://user:pass@localhost:5432/dashboard?sslmode=disable", want: "localhost:5432"},
		{name: "kv", dsn: "host=db.internal port=6543 dbname=dashboard user=test", want: "db.internal:6543"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresAddress(tt.dsn); got != tt.want {
				t.Fatalf("postgresAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizePostgresDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{name: "empty", dsn: "", want: ""},
		{name: "url with db", dsn: "postgres://user:pass@localhost:5432/dashboard?sslmode=disable", want: "postgres://localhost:5432/dashboard"},
		{name: "kv with db", dsn: "host=db.internal port=6543 dbname=dashboard user=test", want: "db.internal:6543/dashboard"},
		{name: "invalid", dsn: "%%%", want: "configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizePostgresDSN(tt.dsn); got != tt.want {
				t.Fatalf("summarizePostgresDSN() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeRedisEndpoint(t *testing.T) {
	got := summarizeRedisEndpoint(config.RedisConfig{URL: "redis://localhost:6379/0"})
	if got != "redis://localhost:6379" {
		t.Fatalf("summarizeRedisEndpoint() = %q, want redis://localhost:6379", got)
	}
}

func TestSplitPSQLRows(t *testing.T) {
	rows := splitPSQLRows("\nrow1\r\n\r\nrow2\n")
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0] != "row1" || rows[1] != "row2" {
		t.Fatalf("rows = %#v, want [row1 row2]", rows)
	}
}

func TestUnmarshalJSONObject(t *testing.T) {
	payload := map[string]any{}
	if err := unmarshalJSONObject(`{"count":3}`, &payload); err != nil {
		t.Fatalf("unmarshalJSONObject() error = %v", err)
	}
	if payload["count"].(float64) != 3 {
		t.Fatalf("payload[count] = %#v, want 3", payload["count"])
	}
}

func TestUnmarshalJSONObjectEmpty(t *testing.T) {
	payload := map[string]string{"existing": "value"}
	if err := unmarshalJSONObject("", &payload); err != nil {
		t.Fatalf("unmarshalJSONObject() error = %v", err)
	}
	if payload["existing"] != "value" {
		t.Fatal("expected empty input to leave target unchanged")
	}
}

func TestPostgresDescriptor(t *testing.T) {
	endpoint := NewPostgresEventStore(config.PostgresConfig{Enabled: true, DSN: "postgres://user:pass@localhost:5432/dashboard"}).Descriptor()
	if endpoint.Transport != "postgresql" {
		t.Fatalf("Transport = %q, want postgresql", endpoint.Transport)
	}
	if endpoint.Endpoint != "postgres://localhost:5432/dashboard" {
		t.Fatalf("Endpoint = %q, want postgres://localhost:5432/dashboard", endpoint.Endpoint)
	}
}
