package model

import "time"

const (
	EnvelopeVersion = 1
	StateChannel    = "dashboard.state"
)

type Agent struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Role             string            `json:"role,omitempty"`
	Status           string            `json:"status"`
	Progress         int               `json:"progress"`
	CurrentTaskID    string            `json:"currentTaskId,omitempty"`
	CurrentTaskTitle string            `json:"currentTaskTitle,omitempty"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type TaskStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type Task struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Description    string            `json:"description,omitempty"`
	Status         string            `json:"status"`
	Progress       int               `json:"progress"`
	CompletedSteps int               `json:"completedSteps,omitempty"`
	TotalSteps     int               `json:"totalSteps,omitempty"`
	Steps          []TaskStep        `json:"steps,omitempty"`
	OwnerAgentIDs  []string          `json:"ownerAgentIds,omitempty"`
	Priority       string            `json:"priority,omitempty"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type Event struct {
	ID        int64             `json:"id"`
	Type      string            `json:"type"`
	Level     string            `json:"level,omitempty"`
	Message   string            `json:"message"`
	AgentID   string            `json:"agentId,omitempty"`
	TaskID    string            `json:"taskId,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	Payload   map[string]any    `json:"payload,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type IntegrationEndpoint struct {
	Transport string `json:"transport"`
	Enabled   bool   `json:"enabled"`
	Planned   bool   `json:"planned"`
	Healthy   bool   `json:"healthy"`
	Endpoint  string `json:"endpoint,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

type Integrations struct {
	SSE        IntegrationEndpoint `json:"sse"`
	WebSocket  IntegrationEndpoint `json:"websocket"`
	NATS       IntegrationEndpoint `json:"nats"`
	Redis      IntegrationEndpoint `json:"redis"`
	PostgreSQL IntegrationEndpoint `json:"postgresql"`
}

type Summary struct {
	Completion int       `json:"completion"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type DashboardState struct {
	Version      int          `json:"version"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	Agents       []Agent      `json:"agents"`
	Tasks        []Task       `json:"tasks"`
	Events       []Event      `json:"events,omitempty"`
	Summary      Summary      `json:"summary"`
	Integrations Integrations `json:"integrations"`
}

type StreamEnvelope struct {
	Version   int             `json:"version"`
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Channel   string          `json:"channel"`
	Kind      string          `json:"kind"`
	Event     *Event          `json:"event,omitempty"`
	State     *DashboardState `json:"state,omitempty"`
}

type Command struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload,omitempty"`
}

type CommandMessage struct {
	Type    string   `json:"type"`
	Command *Command `json:"command,omitempty"`
}

type CommandAck struct {
	Type      string    `json:"type"`
	CommandID string    `json:"commandId,omitempty"`
	Name      string    `json:"name,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
