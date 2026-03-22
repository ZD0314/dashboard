package bus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/model"
)

type EventBus interface {
	Publish(ctx context.Context, envelope model.StreamEnvelope) error
	Subscribe(ctx context.Context) <-chan model.StreamEnvelope
	HealthCheck(ctx context.Context) error
	Descriptor() model.IntegrationEndpoint
}

type MemoryBus struct {
	mu          sync.RWMutex
	subscribers map[chan model.StreamEnvelope]struct{}
}

type NATSBus struct {
	cfg config.NATSConfig
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subscribers: make(map[chan model.StreamEnvelope]struct{})}
}

func NewNATSBus(cfg config.NATSConfig) *NATSBus {
	return &NATSBus{cfg: cfg}
}

func (b *MemoryBus) Publish(_ context.Context, envelope model.StreamEnvelope) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- cloneEnvelope(envelope):
		default:
		}
	}
	return nil
}

func (b *MemoryBus) Subscribe(ctx context.Context) <-chan model.StreamEnvelope {
	ch := make(chan model.StreamEnvelope, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

func (b *MemoryBus) HealthCheck(_ context.Context) error {
	return nil
}

func (b *MemoryBus) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "memory", Enabled: true, Planned: false, Healthy: true, Notes: "In-memory realtime broadcaster"}
}

func (b *NATSBus) Publish(ctx context.Context, envelope model.StreamEnvelope) error {
	conn, err := b.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := expectNATSConnectOK(conn); err != nil {
		return err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(conn, "PUB %s %d\r\n", natsSubject, len(payload)); err != nil {
		return err
	}
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("\r\n")); err != nil {
		return err
	}
	return nil
}

func (b *NATSBus) Subscribe(ctx context.Context) <-chan model.StreamEnvelope {
	ch := make(chan model.StreamEnvelope, 16)
	go b.runSubscription(ctx, ch)
	return ch
}

func (b *NATSBus) HealthCheck(ctx context.Context) error {
	conn, err := b.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return expectNATSConnectOK(conn)
}

func (b *NATSBus) Descriptor() model.IntegrationEndpoint {
	return model.IntegrationEndpoint{Transport: "nats", Enabled: b.cfg.Enabled, Planned: true, Endpoint: summarizeNATSEndpoint(b.cfg.URL), Notes: "Uses NATS core PUB/SUB on dashboard.state"}
}

func (b *NATSBus) runSubscription(ctx context.Context, ch chan<- model.StreamEnvelope) {
	defer close(ch)
	conn, err := b.dial(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	if err := expectNATSConnectOK(conn); err != nil {
		return
	}
	if _, err := fmt.Fprintf(conn, "SUB %s %s 1\r\n", natsSubject, natsSubscriptionID); err != nil {
		return
	}
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return
	}
	if err := conn.SetReadDeadline(nextNATSDeadline(ctx)); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := readNATSLine(reader)
		if err != nil {
			if isExpectedNATSClose(err, ctx) {
				return
			}
			return
		}
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "PING"):
			if _, err := conn.Write([]byte("PONG\r\n")); err != nil {
				return
			}
		case strings.HasPrefix(line, "PONG") || strings.HasPrefix(line, "+OK") || strings.HasPrefix(line, "INFO "):
			continue
		case strings.HasPrefix(line, "-ERR"):
			return
		case strings.HasPrefix(line, "MSG "):
			envelope, err := readNATSMessage(reader, line)
			if err != nil {
				return
			}
			select {
			case ch <- envelope:
			case <-ctx.Done():
				return
			}
		case ctx.Err() != nil:
			return
		}
		if err := conn.SetReadDeadline(nextNATSDeadline(ctx)); err != nil {
			return
		}
	}
}

func (b *NATSBus) dial(ctx context.Context) (net.Conn, error) {
	address := natsAddress(b.cfg.URL)
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("endpoint not configured")
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return dialer.DialContext(ctx, "tcp", address)
}

func cloneEnvelope(envelope model.StreamEnvelope) model.StreamEnvelope {
	cloned := envelope
	if envelope.Event != nil {
		event := *envelope.Event
		cloned.Event = &event
	}
	if envelope.State != nil {
		state := *envelope.State
		state.Agents = append([]model.Agent(nil), envelope.State.Agents...)
		state.Tasks = append([]model.Task(nil), envelope.State.Tasks...)
		state.Events = append([]model.Event(nil), envelope.State.Events...)
		cloned.State = &state
	}
	return cloned
}

func natsAddress(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func summarizeNATSEndpoint(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
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

func SummarizeNATSConfig(cfg config.NATSConfig) string {
	return summarizeNATSEndpoint(cfg.URL)
}

const (
	natsSubject        = model.StateChannel
	natsSubscriptionID = "dashboard"
)

func expectNATSConnectOK(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	for {
		line, err := readNATSLine(reader)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "INFO "):
			if _, err := conn.Write([]byte("CONNECT {}\r\nPING\r\n")); err != nil {
				return err
			}
		case line == "PONG" || line == "+OK":
			return nil
		case strings.HasPrefix(line, "-ERR"):
			return errors.New(strings.TrimSpace(strings.TrimPrefix(line, "-ERR")))
		}
	}
}

func readNATSMessage(reader *bufio.Reader, header string) (model.StreamEnvelope, error) {
	parts := strings.Fields(header)
	if len(parts) < 4 {
		return model.StreamEnvelope{}, fmt.Errorf("invalid nats msg header: %s", header)
	}
	size, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return model.StreamEnvelope{}, err
	}
	payload := make([]byte, size+2)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return model.StreamEnvelope{}, err
	}
	var envelope model.StreamEnvelope
	if err := json.Unmarshal(payload[:size], &envelope); err != nil {
		return model.StreamEnvelope{}, err
	}
	return cloneEnvelope(envelope), nil
}

func readNATSLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func nextNATSDeadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(30 * time.Second)
}

func isExpectedNATSClose(err error, ctx context.Context) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
