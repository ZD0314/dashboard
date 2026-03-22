package bus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/model"
)

func TestNATSAddress(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "valid url", raw: "nats://127.0.0.1:4222", want: "127.0.0.1:4222"},
		{name: "invalid url", raw: "://bad", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := natsAddress(tt.raw); got != tt.want {
				t.Fatalf("natsAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeNATSEndpoint(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "valid url", raw: "nats://localhost:4222", want: "nats://localhost:4222"},
		{name: "invalid url", raw: "bad://:::", want: "bad://:::"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeNATSEndpoint(tt.raw); got != tt.want {
				t.Fatalf("summarizeNATSEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadNATSMessage(t *testing.T) {
	payload := `{"version":1,"seq":7,"channel":"dashboard.state","kind":"update"}`
	reader := bufio.NewReader(strings.NewReader(payload + "\r\n"))
	envelope, err := readNATSMessage(reader, fmt.Sprintf("MSG dashboard.state dashboard %d", len(payload)))
	if err != nil {
		t.Fatalf("readNATSMessage() error = %v", err)
	}
	if envelope.Seq != 7 {
		t.Fatalf("envelope.Seq = %d, want 7", envelope.Seq)
	}
	if envelope.Kind != "update" {
		t.Fatalf("envelope.Kind = %q, want update", envelope.Kind)
	}
}

func TestReadNATSMessageRejectsBadHeader(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("{}\r\n"))
	_, err := readNATSMessage(reader, "MSG broken")
	if err == nil {
		t.Fatal("expected error for invalid header")
	}
}

func TestExpectNATSConnectOK(t *testing.T) {
	server, client := netPipeWithServer(func(conn net.Conn) {
		defer conn.Close()
		_, _ = conn.Write([]byte("INFO {\"server_id\":\"test\"}\r\n"))
		buf := make([]byte, 64)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("PONG\r\n"))
	})
	defer server.Close()
	defer client.Close()

	if err := expectNATSConnectOK(client); err != nil {
		t.Fatalf("expectNATSConnectOK() error = %v", err)
	}
}

func TestIsExpectedNATSClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isExpectedNATSClose(errors.New("closed"), ctx) {
		t.Fatal("expected closed context to be treated as expected close")
	}
}

func TestNATSBusDescriptor(t *testing.T) {
	endpoint := NewNATSBus(config.NATSConfig{Enabled: true, URL: "nats://localhost:4222"}).Descriptor()
	if endpoint.Transport != "nats" {
		t.Fatalf("Transport = %q, want nats", endpoint.Transport)
	}
	if endpoint.Endpoint != "nats://localhost:4222" {
		t.Fatalf("Endpoint = %q, want nats://localhost:4222", endpoint.Endpoint)
	}
}

func TestCloneEnvelopeClonesNestedState(t *testing.T) {
	event := &model.Event{ID: 1, Message: "hello"}
	state := &model.DashboardState{Agents: []model.Agent{{ID: "a1"}}, Tasks: []model.Task{{ID: "t1"}}, Events: []model.Event{{ID: 2}}}
	original := model.StreamEnvelope{Event: event, State: state}
	cloned := cloneEnvelope(original)

	cloned.Event.Message = "changed"
	cloned.State.Agents[0].ID = "changed"

	if original.Event.Message != "hello" {
		t.Fatal("expected event to be cloned")
	}
	if original.State.Agents[0].ID != "a1" {
		t.Fatal("expected state slices to be cloned")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func netPipeWithServer(serverFn func(net.Conn)) (net.Conn, net.Conn) {
	server, client := net.Pipe()
	go serverFn(server)
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	return server, client
}
