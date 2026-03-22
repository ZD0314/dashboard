package httpserver

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dashboard/internal/model"
	"dashboard/internal/provider"
)

type Server struct {
	provider  provider.StateProvider
	staticDir string
}

func NewServer(p provider.StateProvider, staticDir string) *Server {
	return &Server{provider: p, staticDir: staticDir}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/app.js", http.FileServer(http.Dir(s.staticDir)))
	mux.Handle("/styles.css", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.staticDir, "styles.css"))
	}))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/ws", s.handleWebSocket)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	indexPath, err := s.resolveIndexPath()
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, indexPath)
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(s.provider.Snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE(w, "snapshot", model.StreamEnvelope{
		Version:   model.EnvelopeVersion,
		Seq:       0,
		Timestamp: time.Now(),
		Channel:   model.StateChannel,
		Kind:      "snapshot",
		State:     ptrState(s.provider.Snapshot()),
	})
	flusher.Flush()

	updates := s.provider.Subscribe(r.Context())
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case envelope, ok := <-updates:
			if !ok {
				return
			}
			eventName := envelope.Kind
			if eventName == "" {
				eventName = "update"
			}
			sendSSE(w, eventName, envelope)
			flusher.Flush()
		case <-heartbeat.C:
			sendSSE(w, "heartbeat", model.StreamEnvelope{
				Version:   model.EnvelopeVersion,
				Seq:       0,
				Timestamp: time.Now(),
				Channel:   model.StateChannel,
				Kind:      "heartbeat",
			})
			flusher.Flush()
		}
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !headerContainsToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing websocket key", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "websocket hijack failed", http.StatusInternalServerError)
		return
	}

	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Sec-WebSocket-Accept: " + websocketAcceptKey(key) + "\r\n\r\n")
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if err := writeWebSocketJSON(conn, model.StreamEnvelope{
		Version:   model.EnvelopeVersion,
		Seq:       0,
		Timestamp: time.Now(),
		Channel:   model.StateChannel,
		Kind:      "snapshot",
		State:     ptrState(s.provider.Snapshot()),
	}); err != nil {
		_ = conn.Close()
		return
	}

	updates := s.provider.Subscribe(ctx)
	errCh := make(chan error, 1)
	go s.readWebSocketLoop(ctx, conn, errCh)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return
		case <-errCh:
			_ = conn.Close()
			return
		case envelope, ok := <-updates:
			if !ok {
				_ = conn.Close()
				return
			}
			if err := writeWebSocketJSON(conn, envelope); err != nil {
				_ = conn.Close()
				return
			}
		case <-heartbeat.C:
			if err := writeWebSocketJSON(conn, model.StreamEnvelope{
				Version:   model.EnvelopeVersion,
				Seq:       0,
				Timestamp: time.Now(),
				Channel:   model.StateChannel,
				Kind:      "heartbeat",
			}); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (s *Server) readWebSocketLoop(ctx context.Context, conn net.Conn, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		payload, opcode, err := readWebSocketFrame(conn)
		if err != nil {
			errCh <- err
			return
		}
		switch opcode {
		case 0x8:
			errCh <- nil
			return
		case 0x9:
			_ = writeWebSocketFrame(conn, 0xA, payload)
		case 0x1:
			var msg model.CommandMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				_ = writeWebSocketJSON(conn, model.CommandAck{Type: "ack", Status: "rejected", Message: "invalid command payload", Timestamp: time.Now()})
				continue
			}
			if msg.Type != "command" || msg.Command == nil {
				_ = writeWebSocketJSON(conn, model.CommandAck{Type: "ack", Status: "rejected", Message: "command message required", Timestamp: time.Now()})
				continue
			}
			_ = writeWebSocketJSON(conn, s.provider.HandleCommand(ctx, *msg.Command))
		}
	}
}

func (s *Server) resolveIndexPath() (string, error) {
	candidates := []string{
		filepath.Join(s.staticDir, "index.html"),
		filepath.Join(filepath.Dir(s.staticDir), "index.html"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func sendSSE(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func ptrState(value model.DashboardState) *model.DashboardState { return &value }

func headerContainsToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func websocketAcceptKey(key string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func writeWebSocketJSON(conn net.Conn, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeWebSocketFrame(conn, 0x1, data)
}

func writeWebSocketFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(length))
		header = append(header, buf...)
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func readWebSocketFrame(conn net.Conn) ([]byte, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, 0, err
	}
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)
	if length == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	} else if length == 127 {
		ext := make([]byte, 8)
		if _, err := io.ReadFull(conn, ext); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(conn, maskKey); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, opcode, nil
}
