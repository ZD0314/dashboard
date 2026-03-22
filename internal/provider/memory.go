package provider

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"dashboard/internal/bus"
	"dashboard/internal/model"
	"dashboard/internal/store"
)

type StateProvider interface {
	Snapshot() model.DashboardState
	Subscribe(ctx context.Context) <-chan model.StreamEnvelope
	HandleCommand(ctx context.Context, cmd model.Command) model.CommandAck
}

type MemoryProvider struct {
	mu                    sync.RWMutex
	state                 model.DashboardState
	stateStore            store.StateStore
	eventStore            store.EventStore
	bus                   bus.EventBus
	nextEventID           int64
	nextSeq               int64
	tick                  int
	lastIntegrationHealth integrationHealth
	healthCheckedAt       time.Time
}

type integrationHealth struct {
	state bool
	event bool
	bus   bool
}

func NewMemoryProvider(stateStore store.StateStore, eventStore store.EventStore, eventBus bus.EventBus) *MemoryProvider {
	now := time.Now()
	initial := loadInitialState(stateStore, now)
	p := &MemoryProvider{
		state:                 initial,
		stateStore:            stateStore,
		eventStore:            eventStore,
		bus:                   eventBus,
		nextEventID:           nextEventID(initial),
		nextSeq:               1,
		lastIntegrationHealth: integrationHealth{state: true, event: true, bus: true},
	}
	if p.stateStore == nil {
		p.stateStore = store.NewMemoryStateStore(initial)
	}
	if p.eventStore == nil {
		p.eventStore = store.NewMemoryEventStore(initial.Events)
	}
	if p.bus == nil {
		p.bus = bus.NewMemoryBus()
	}
	p.refreshIntegrations(context.Background())
	if err := p.stateStore.Save(context.Background(), p.state); err != nil {
		log.Printf("provider: initial state save failed: %v", err)
	}
	for _, event := range p.state.Events {
		if err := p.eventStore.Append(context.Background(), event); err != nil {
			log.Printf("provider: initial event append failed: event_id=%d err=%v", event.ID, err)
		}
	}
	go p.runSimulation()
	return p
}

func (p *MemoryProvider) Snapshot() model.DashboardState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneState(p.state)
}

func (p *MemoryProvider) Subscribe(ctx context.Context) <-chan model.StreamEnvelope {
	return p.bus.Subscribe(ctx)
}

func (p *MemoryProvider) HandleCommand(_ context.Context, cmd model.Command) model.CommandAck {
	ack := model.CommandAck{
		Type:      "ack",
		CommandID: cmd.ID,
		Name:      cmd.Name,
		Status:    "accepted",
		Timestamp: time.Now(),
	}
	if cmd.Name == "" {
		ack.Status = "rejected"
		ack.Message = "command name is required"
		return ack
	}
	switch cmd.Name {
	case "ping":
		ack.Message = "pong"
	case "pause_agent", "resume_agent", "reassign_task":
		ack.Message = "command accepted by skeleton handler"
	default:
		ack.Status = "ignored"
		ack.Message = "command not implemented yet"
	}
	return ack
}

func (p *MemoryProvider) runSimulation() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		health := p.cachedIntegrationHealth(now)
		p.mu.Lock()
		p.tick++

		taskIndex := p.tick % len(p.state.Tasks)
		for i := range p.state.Tasks {
			task := &p.state.Tasks[i]
			if i == taskIndex {
				advanceTask(task, now)
			} else if task.Status == "running" {
				task.Status = "pending"
				task.UpdatedAt = now
			}
		}

		currentTask := &p.state.Tasks[taskIndex]
		for i := range p.state.Agents {
			agent := &p.state.Agents[i]
			agent.CurrentTaskID = currentTask.ID
			agent.CurrentTaskTitle = currentTask.Title
			agent.UpdatedAt = now
			switch agent.Role {
			case "coordinator":
				agent.Progress = clamp(currentTask.Progress + 10)
				agent.Status = "running"
			case "backend":
				agent.Progress = clamp(currentTask.Progress + 15)
				if currentTask.Status == "done" {
					agent.Status = "idle"
				} else {
					agent.Status = "running"
				}
			case "frontend":
				agent.Progress = clamp(currentTask.Progress / 2)
				if currentTask.Progress > 60 {
					agent.Status = "running"
				} else {
					agent.Status = "idle"
				}
			default:
				agent.Progress = currentTask.Progress
			}
		}

		p.state.Summary.Completion = summarizeProgress(p.state.Tasks)
		p.state.Summary.Status = summarizeStatus(p.state.Tasks)
		if currentTask.Status == "done" {
			p.state.Summary.Message = fmt.Sprintf("%s completed, ready for next adapter", currentTask.Title)
		} else {
			p.state.Summary.Message = fmt.Sprintf("%s advanced to %d%%", currentTask.Title, currentTask.Progress)
		}
		p.state.Summary.UpdatedAt = now
		p.state.UpdatedAt = now
		p.refreshIntegrationsLocked(health)

		eventType := "task.progress"
		message := fmt.Sprintf("%s advanced to %d/%d steps", currentTask.Title, currentTask.CompletedSteps, currentTask.TotalSteps)
		if currentTask.Status == "done" {
			eventType = "task.done"
			message = fmt.Sprintf("%s completed", currentTask.Title)
		}
		event := p.appendEventLocked(model.Event{
			Type:      eventType,
			Level:     "info",
			Message:   message,
			AgentID:   "backend-agent",
			TaskID:    currentTask.ID,
			CreatedAt: now,
			Payload: map[string]any{
				"progress":       currentTask.Progress,
				"completedSteps": currentTask.CompletedSteps,
				"totalSteps":     currentTask.TotalSteps,
			},
			Metadata: map[string]string{
				"source": "simulation",
			},
		})

		envelope := model.StreamEnvelope{
			Version:   model.EnvelopeVersion,
			Seq:       p.nextSequenceLocked(),
			Timestamp: now,
			Channel:   model.StateChannel,
			Kind:      "update",
			Event:     ptrEvent(event),
			State:     ptrState(cloneState(p.state)),
		}
		stateSnapshot := cloneState(p.state)
		p.mu.Unlock()

		if err := p.stateStore.Save(context.Background(), stateSnapshot); err != nil {
			log.Printf("provider: state save failed: err=%v", err)
		}
		if err := p.eventStore.Append(context.Background(), event); err != nil {
			log.Printf("provider: event append failed: event_id=%d type=%s err=%v", event.ID, event.Type, err)
		}
		if err := p.bus.Publish(context.Background(), envelope); err != nil {
			log.Printf("provider: bus publish failed: seq=%d channel=%s err=%v", envelope.Seq, envelope.Channel, err)
		}
	}
}

func (p *MemoryProvider) appendEventLocked(event model.Event) model.Event {
	event.ID = p.nextEventID
	p.nextEventID++
	p.state.Events = append([]model.Event{event}, p.state.Events...)
	if len(p.state.Events) > 50 {
		p.state.Events = p.state.Events[:50]
	}
	return event
}

func (p *MemoryProvider) nextSequenceLocked() int64 {
	seq := p.nextSeq
	p.nextSeq++
	return seq
}

func (p *MemoryProvider) refreshIntegrations(ctx context.Context) {
	health := p.integrationHealth(ctx)
	p.mu.Lock()
	p.lastIntegrationHealth = health
	p.healthCheckedAt = time.Now()
	defer p.mu.Unlock()
	p.refreshIntegrationsLocked(health)
}

func (p *MemoryProvider) cachedIntegrationHealth(now time.Time) integrationHealth {
	p.mu.RLock()
	last := p.lastIntegrationHealth
	checkedAt := p.healthCheckedAt
	p.mu.RUnlock()
	if !checkedAt.IsZero() && now.Sub(checkedAt) < 10*time.Second {
		return last
	}
	health := p.integrationHealth(context.Background())
	p.mu.Lock()
	p.lastIntegrationHealth = health
	p.healthCheckedAt = now
	p.mu.Unlock()
	return health
}

func (p *MemoryProvider) refreshIntegrationsLocked(health integrationHealth) {
	p.state.Integrations = model.Integrations{
		SSE: model.IntegrationEndpoint{Transport: "sse", Enabled: true, Planned: false, Healthy: true, Endpoint: "/api/events", Notes: "Default realtime stream"},
		WebSocket: model.IntegrationEndpoint{Transport: "websocket", Enabled: true, Planned: true, Healthy: true, Endpoint: "/ws", Notes: "Browser duplex channel with command ack skeleton"},
		Redis: mergeHealth(p.stateStore.Descriptor(), health.state),
		PostgreSQL: mergeHealth(p.eventStore.Descriptor(), health.event),
		NATS: mergeHealth(p.bus.Descriptor(), health.bus),
	}
}

func (p *MemoryProvider) integrationHealth(ctx context.Context) integrationHealth {
	return integrationHealth{
		state: p.stateStore.HealthCheck(ctx) == nil,
		event: p.eventStore.HealthCheck(ctx) == nil,
		bus:   p.bus.HealthCheck(ctx) == nil,
	}
}

func mergeHealth(endpoint model.IntegrationEndpoint, healthy bool) model.IntegrationEndpoint {
	endpoint.Healthy = healthy
	return endpoint
}

func loadInitialState(stateStore store.StateStore, now time.Time) model.DashboardState {
	seed := seedState(now)
	if stateStore == nil {
		return seed
	}
	loaded, err := stateStore.Load(context.Background())
	if err != nil {
		return seed
	}
	loaded.Integrations = seed.Integrations
	return loaded
}

func nextEventID(state model.DashboardState) int64 {
	var maxID int64
	for _, event := range state.Events {
		if event.ID > maxID {
			maxID = event.ID
		}
	}
	if maxID == 0 {
		return 1
	}
	return maxID + 1
}

func seedState(now time.Time) model.DashboardState {
	tasks := []model.Task{
		{
			ID:             "task-realtime-contract",
			Title:          "Unify realtime envelope",
			Description:    "收敛 snapshot/update/heartbeat 的统一协议与输出路径。",
			Status:         "running",
			Progress:       45,
			CompletedSteps: 2,
			TotalSteps:     4,
			Steps: []model.TaskStep{{ID: "step-model", Title: "统一 model", Done: true}, {ID: "step-provider", Title: "统一 provider 输出", Done: true}, {ID: "step-http", Title: "对齐 SSE/WS", Done: false}, {ID: "step-verify", Title: "验证前端渲染", Done: false}},
			OwnerAgentIDs: []string{"team-lead", "backend-agent"},
			Priority:      "high",
			UpdatedAt:     now,
		},
		{
			ID:             "task-store-bus-skeleton",
			Title:          "Add store and bus skeletons",
			Description:    "为 Redis/PostgreSQL/NATS 建立最小可插拔骨架。",
			Status:         "pending",
			Progress:       20,
			CompletedSteps: 1,
			TotalSteps:     4,
			Steps: []model.TaskStep{{ID: "step-config", Title: "配置骨架", Done: true}, {ID: "step-store", Title: "store 接口", Done: false}, {ID: "step-bus", Title: "bus 接口", Done: false}, {ID: "step-health", Title: "健康检查", Done: false}},
			OwnerAgentIDs: []string{"backend-agent"},
			Priority:      "high",
			UpdatedAt:     now,
		},
		{
			ID:             "task-ui-transport",
			Title:          "Split frontend transport adapters",
			Description:    "拆分 fetch/SSE/WebSocket 连接逻辑并保持回退能力。",
			Status:         "pending",
			Progress:       15,
			CompletedSteps: 1,
			TotalSteps:     5,
			Steps: []model.TaskStep{{ID: "step-fetch", Title: "初始状态加载", Done: true}, {ID: "step-sse", Title: "SSE 连接器", Done: false}, {ID: "step-ws", Title: "WebSocket 连接器", Done: false}, {ID: "step-consume", Title: "统一 envelope 消费", Done: false}, {ID: "step-fallback", Title: "回退策略", Done: false}},
			OwnerAgentIDs: []string{"frontend-agent"},
			Priority:      "medium",
			UpdatedAt:     now,
		},
	}
	state := model.DashboardState{
		Version:   model.EnvelopeVersion,
		UpdatedAt: now,
		Agents: []model.Agent{
			{ID: "team-lead", Name: "Team Lead", Role: "coordinator", Status: "running", Progress: 52, CurrentTaskID: tasks[0].ID, CurrentTaskTitle: tasks[0].Title, UpdatedAt: now, Metadata: map[string]string{"team": "dashboard-panel"}},
			{ID: "backend-agent", Name: "Backend Agent", Role: "backend", Status: "running", Progress: 64, CurrentTaskID: tasks[0].ID, CurrentTaskTitle: tasks[0].Title, UpdatedAt: now},
			{ID: "frontend-agent", Name: "Frontend Agent", Role: "frontend", Status: "idle", Progress: 24, CurrentTaskID: tasks[2].ID, CurrentTaskTitle: tasks[2].Title, UpdatedAt: now},
		},
		Tasks: tasks,
		Summary: model.Summary{
			Completion: 27,
			Status:     "running",
			Message:    "Dashboard realtime enhancement in progress",
			UpdatedAt:  now,
		},
	}
	bootEvent := model.Event{
		ID:        1,
		Type:      "system.boot",
		Level:     "info",
		Message:   "dashboard memory provider started",
		CreatedAt: now,
		Metadata: map[string]string{
			"source": "memory",
		},
	}
	state.Events = []model.Event{bootEvent}
	state.Integrations = model.Integrations{
		SSE: model.IntegrationEndpoint{Transport: "sse", Enabled: true, Planned: false, Healthy: true, Endpoint: "/api/events", Notes: "Default realtime stream"},
		WebSocket: model.IntegrationEndpoint{Transport: "websocket", Enabled: true, Planned: true, Healthy: true, Endpoint: "/ws", Notes: "Browser duplex channel with command ack skeleton"},
		Redis: model.IntegrationEndpoint{Transport: "redis", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for snapshot cache and hot state"},
		PostgreSQL: model.IntegrationEndpoint{Transport: "postgresql", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for event history and audit persistence"},
		NATS: model.IntegrationEndpoint{Transport: "nats", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for multi-process event fan-out"},
	}
	return state
}

func advanceTask(task *model.Task, now time.Time) {
	if task.TotalSteps <= 0 {
		task.TotalSteps = len(task.Steps)
	}
	if task.TotalSteps <= 0 {
		task.TotalSteps = 1
	}
	nextDone := (task.CompletedSteps + 1) % (task.TotalSteps + 1)
	for i := range task.Steps {
		task.Steps[i].Done = i < nextDone
	}
	task.CompletedSteps = nextDone
	task.Progress = clamp(int(float64(task.CompletedSteps) / float64(task.TotalSteps) * 100))
	if task.CompletedSteps >= task.TotalSteps {
		task.Status = "done"
	} else {
		task.Status = "running"
	}
	task.UpdatedAt = now
}

func summarizeProgress(tasks []model.Task) int {
	if len(tasks) == 0 {
		return 0
	}
	total := 0
	for _, task := range tasks {
		total += clamp(task.Progress)
	}
	return clamp(total / len(tasks))
}

func summarizeStatus(tasks []model.Task) string {
	hasRunning := false
	hasBlocked := false
	allDone := len(tasks) > 0
	for _, task := range tasks {
		switch task.Status {
		case "running":
			hasRunning = true
		case "blocked":
			hasBlocked = true
		case "done":
		default:
			allDone = false
		}
		if task.Status != "done" {
			allDone = false
		}
	}
	if hasBlocked {
		return "blocked"
	}
	if hasRunning {
		return "running"
	}
	if allDone {
		return "done"
	}
	return "idle"
}

func clamp(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func cloneState(state model.DashboardState) model.DashboardState {
	cloned := state
	cloned.Agents = append([]model.Agent(nil), state.Agents...)
	cloned.Tasks = append([]model.Task(nil), state.Tasks...)
	cloned.Events = append([]model.Event(nil), state.Events...)
	return cloned
}

func ptrState(value model.DashboardState) *model.DashboardState {
	return &value
}

func ptrEvent(value model.Event) *model.Event {
	return &value
}
