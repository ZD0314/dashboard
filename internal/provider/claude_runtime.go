package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/bus"
	"dashboard/internal/model"
)

// ClaudeRuntimeProvider reads real team/task state from all Claude local team files.
type ClaudeRuntimeProvider struct {
	mu           sync.RWMutex
	teamsDir     string
	tasksDir     string
	pollInterval time.Duration
	bus          bus.EventBus

	state       model.DashboardState
	nextEventID int64
	nextSeq     int64

	lastTaskStatuses map[string]string
	lastTaskOwners   map[string]string
}

func NewClaudeRuntimeProvider(teamsDir, tasksDir string, pollInterval time.Duration, eventBus bus.EventBus) *ClaudeRuntimeProvider {
	if eventBus == nil {
		eventBus = bus.NewMemoryBus()
	}
	p := &ClaudeRuntimeProvider{
		teamsDir:         teamsDir,
		tasksDir:         tasksDir,
		pollInterval:     pollInterval,
		bus:              eventBus,
		nextEventID:      1,
		nextSeq:          1,
		lastTaskStatuses: make(map[string]string),
		lastTaskOwners:   make(map[string]string),
	}
	now := time.Now()
	p.state = p.buildState(now)
	bootEvent := model.Event{
		ID:        p.nextEventID,
		Type:      "system.boot",
		Level:     "info",
		Message:   "claude-runtime provider started",
		CreatedAt: now,
		Metadata:  map[string]string{"source": "claude-files"},
	}
	p.nextEventID++
	p.state.Events = []model.Event{bootEvent}
	for _, t := range p.state.Tasks {
		p.lastTaskStatuses[t.ID] = t.Status
		if len(t.OwnerAgentIDs) > 0 {
			p.lastTaskOwners[t.ID] = t.OwnerAgentIDs[0]
		}
	}
	go p.runPoller()
	return p
}

func (p *ClaudeRuntimeProvider) Snapshot() model.DashboardState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneState(p.state)
}

func (p *ClaudeRuntimeProvider) Subscribe(ctx context.Context) <-chan model.StreamEnvelope {
	return p.bus.Subscribe(ctx)
}

func (p *ClaudeRuntimeProvider) HandleCommand(_ context.Context, cmd model.Command) model.CommandAck {
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
	default:
		ack.Status = "ignored"
		ack.Message = "command not implemented in claude-runtime provider"
	}
	return ack
}

func (p *ClaudeRuntimeProvider) runPoller() {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		newState := p.buildState(now)

		p.mu.Lock()
		events := p.diffEvents(newState, now)
		for i := range events {
			events[i].ID = p.nextEventID
			p.nextEventID++
		}
		newState.Events = mergeEvents(events, p.state.Events)
		p.state = newState
		stateSnapshot := cloneState(p.state)
		p.mu.Unlock()

		for _, event := range events {
			ev := event
			envelope := model.StreamEnvelope{
				Version:   model.EnvelopeVersion,
				Seq:       p.nextSeqVal(),
				Timestamp: now,
				Channel:   model.StateChannel,
				Kind:      "update",
				Event:     &ev,
				State:     &stateSnapshot,
			}
			if err := p.bus.Publish(context.Background(), envelope); err != nil {
				log.Printf("claude-runtime: bus publish failed: %v", err)
			}
		}

		if len(events) == 0 {
			envelope := model.StreamEnvelope{
				Version:   model.EnvelopeVersion,
				Seq:       p.nextSeqVal(),
				Timestamp: now,
				Channel:   model.StateChannel,
				Kind:      "update",
				State:     &stateSnapshot,
			}
			if err := p.bus.Publish(context.Background(), envelope); err != nil {
				log.Printf("claude-runtime: bus publish failed: %v", err)
			}
		}
	}
}

func (p *ClaudeRuntimeProvider) nextSeqVal() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := p.nextSeq
	p.nextSeq++
	return seq
}

// --- data types ---

type claudeTeamConfig struct {
	Name    string         `json:"name"`
	Members []claudeMember `json:"members"`
}

type claudeMember struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	AgentType string `json:"agentType"`
	Model     string `json:"model"`
	CWD       string `json:"cwd"`
}

type claudeTaskFile struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm"`
	Status      string         `json:"status"`
	Owner       string         `json:"owner"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata"`
	modTime     time.Time      // populated from file stat, not JSON
}

// --- multi-team scanning ---

type teamData struct {
	name   string
	config claudeTeamConfig
	tasks  []claudeTaskFile
}

func (p *ClaudeRuntimeProvider) scanAllTeams() []teamData {
	entries, err := os.ReadDir(p.teamsDir)
	if err != nil {
		log.Printf("claude-runtime: cannot read teams dir %s: %v", p.teamsDir, err)
		return nil
	}

	var teams []teamData
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		teamName := entry.Name()
		configPath := filepath.Join(p.teamsDir, teamName, "config.json")
		cfg := loadTeamConfig(configPath)
		if cfg.Name == "" {
			cfg.Name = teamName
		}
		taskDir := filepath.Join(p.tasksDir, teamName)
		tasks := loadTasks(taskDir)
		teams = append(teams, teamData{name: teamName, config: cfg, tasks: tasks})
	}
	return teams
}

func loadTeamConfig(path string) claudeTeamConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return claudeTeamConfig{}
	}
	var cfg claudeTeamConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("claude-runtime: cannot parse team config %s: %v", path, err)
		return claudeTeamConfig{}
	}
	return cfg
}

func loadTasks(taskDir string) []claudeTaskFile {
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -7) // only tasks modified in last 7 days
	var tasks []claudeTaskFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(taskDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// skip files older than 7 days unless they are active
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var t claudeTaskFile
		if err := json.Unmarshal(data, &t); err != nil {
			log.Printf("claude-runtime: cannot parse task %s: %v", entry.Name(), err)
			continue
		}
		if v, ok := t.Metadata["_internal"]; ok && v == true {
			continue
		}
		// always include active tasks; skip old completed/pending ones
		isActive := t.Status == "in_progress" || t.Status == "pending"
		if !isActive && info.ModTime().Before(cutoff) {
			continue
		}
		t.modTime = info.ModTime()
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})
	return tasks
}

// --- state building ---

func (p *ClaudeRuntimeProvider) buildState(now time.Time) model.DashboardState {
	teams := p.scanAllTeams()

	var allAgents []model.Agent
	var allTasks []model.Task
	seenAgents := make(map[string]bool)

	for _, td := range teams {
		agents := buildAgentsForTeam(td.name, td.config, td.tasks, now)
		tasks := buildTasksForTeam(td.name, td.tasks, now)

		for _, a := range agents {
			if !seenAgents[a.ID] {
				seenAgents[a.ID] = true
				allAgents = append(allAgents, a)
			}
		}
		allTasks = append(allTasks, tasks...)
	}

	summary := buildSummary(allTasks, len(teams), now)

	return model.DashboardState{
		Version:   model.EnvelopeVersion,
		UpdatedAt: now,
		Agents:    allAgents,
		Tasks:     allTasks,
		Summary:   summary,
		Integrations: model.Integrations{
			SSE:        model.IntegrationEndpoint{Transport: "sse", Enabled: true, Planned: false, Healthy: true, Endpoint: "/api/events", Notes: "Default realtime stream"},
			WebSocket:  model.IntegrationEndpoint{Transport: "websocket", Enabled: true, Planned: true, Healthy: true, Endpoint: "/ws", Notes: "Browser duplex channel"},
			Redis:      model.IntegrationEndpoint{Transport: "redis", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for snapshot cache"},
			PostgreSQL: model.IntegrationEndpoint{Transport: "postgresql", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for event history"},
			NATS:       model.IntegrationEndpoint{Transport: "nats", Enabled: false, Planned: true, Healthy: false, Notes: "Reserved for multi-process fan-out"},
		},
	}
}

func buildAgentsForTeam(teamName string, cfg claudeTeamConfig, tasks []claudeTaskFile, now time.Time) []model.Agent {
	ownerTasks := make(map[string][]claudeTaskFile)
	for _, t := range tasks {
		if t.Owner != "" {
			ownerTasks[t.Owner] = append(ownerTasks[t.Owner], t)
		}
	}

	var agents []model.Agent
	for _, m := range cfg.Members {
		if m.Name == "" {
			continue
		}
		// use "teamName/memberName" as ID to avoid cross-team collisions
		agentID := teamName + "/" + m.Name
		agent := model.Agent{
			ID:        agentID,
			Name:      m.Name,
			Role:      m.AgentType,
			UpdatedAt: now,
			Metadata: map[string]string{
				"agentId": m.AgentID,
				"model":   m.Model,
				"team":    teamName,
			},
		}
		if m.CWD != "" {
			agent.Metadata["cwd"] = m.CWD
		}

		ownedTasks := ownerTasks[m.Name]
		activeTask := findActiveTask(ownedTasks)
		if activeTask != nil {
			agent.CurrentTaskID = teamName + "/" + activeTask.ID
			agent.CurrentTaskTitle = activeTask.Subject
			agent.Status = mapTaskStatusToAgentStatus(activeTask.Status)
			agent.Progress = taskStatusToProgress(activeTask.Status)
		} else {
			agent.Status = "idle"
		}
		agents = append(agents, agent)
	}
	return agents
}

func buildTasksForTeam(teamName string, tasks []claudeTaskFile, now time.Time) []model.Task {
	var result []model.Task
	for _, t := range tasks {
		updatedAt := now
		if !t.modTime.IsZero() {
			updatedAt = t.modTime
		}
		mt := model.Task{
			ID:          teamName + "/" + t.ID,
			Title:       t.Subject,
			Description: t.Description,
			Status:      mapClaudeTaskStatus(t.Status),
			UpdatedAt:   updatedAt,
			Metadata:    map[string]string{"team": teamName},
		}
		if t.Owner != "" {
			mt.OwnerAgentIDs = []string{teamName + "/" + t.Owner}
		}
		switch t.Status {
		case "completed":
			mt.Progress = 100
			mt.CompletedSteps = 1
			mt.TotalSteps = 1
		case "in_progress":
			mt.Progress = 50
			mt.TotalSteps = 1
		default:
			mt.TotalSteps = 1
		}
		if t.ActiveForm != "" {
			mt.Metadata["activeForm"] = t.ActiveForm
		}
		result = append(result, mt)
	}
	return result
}

// --- helpers ---

func findActiveTask(tasks []claudeTaskFile) *claudeTaskFile {
	for i := range tasks {
		if tasks[i].Status == "in_progress" {
			return &tasks[i]
		}
	}
	for i := range tasks {
		if tasks[i].Status == "pending" {
			return &tasks[i]
		}
	}
	return nil
}

func mapTaskStatusToAgentStatus(s string) string {
	switch s {
	case "in_progress":
		return "running"
	case "blocked":
		return "blocked"
	default:
		return "idle"
	}
}

func taskStatusToProgress(s string) int {
	switch s {
	case "in_progress":
		return 50
	case "completed":
		return 100
	default:
		return 0
	}
}

func mapClaudeTaskStatus(s string) string {
	switch s {
	case "in_progress":
		return "running"
	case "completed":
		return "done"
	case "pending":
		return "pending"
	case "blocked":
		return "blocked"
	default:
		return s
	}
}

func buildSummary(tasks []model.Task, teamCount int, now time.Time) model.Summary {
	running, done := 0, 0
	for _, t := range tasks {
		if t.Status == "running" {
			running++
		}
		if t.Status == "done" {
			done++
		}
	}

	var msg string
	if running > 0 {
		msg = fmt.Sprintf("%d 个 team，%d 个任务进行中", teamCount, running)
	} else if len(tasks) > 0 && done == len(tasks) {
		msg = fmt.Sprintf("%d 个 team，所有任务已完成", teamCount)
	} else {
		msg = fmt.Sprintf("%d 个 team，共 %d 个任务", teamCount, len(tasks))
	}

	return model.Summary{
		Completion: summarizeProgress(tasks),
		Status:     summarizeStatus(tasks),
		Message:    msg,
		UpdatedAt:  now,
	}
}

func (p *ClaudeRuntimeProvider) diffEvents(newState model.DashboardState, now time.Time) []model.Event {
	var events []model.Event

	newStatuses := make(map[string]string)
	newOwners := make(map[string]string)
	for _, t := range newState.Tasks {
		newStatuses[t.ID] = t.Status
		if len(t.OwnerAgentIDs) > 0 {
			newOwners[t.ID] = t.OwnerAgentIDs[0]
		}
	}

	for _, t := range newState.Tasks {
		oldStatus, existed := p.lastTaskStatuses[t.ID]
		if !existed {
			events = append(events, model.Event{
				Type:      "task.added",
				Level:     "info",
				Message:   fmt.Sprintf("任务新增: %s", t.Title),
				TaskID:    t.ID,
				CreatedAt: now,
				Metadata:  map[string]string{"source": "claude-files", "team": t.Metadata["team"]},
			})
		} else if oldStatus != t.Status {
			events = append(events, model.Event{
				Type:      "task.status",
				Level:     "info",
				Message:   fmt.Sprintf("任务状态变更: %s → %s (%s)", oldStatus, t.Status, t.Title),
				TaskID:    t.ID,
				CreatedAt: now,
				Metadata:  map[string]string{"source": "claude-files", "team": t.Metadata["team"]},
			})
		}

		if existed && p.lastTaskOwners[t.ID] != newOwners[t.ID] {
			events = append(events, model.Event{
				Type:      "task.owner",
				Level:     "info",
				Message:   fmt.Sprintf("任务负责人变更: %s → %s (%s)", p.lastTaskOwners[t.ID], newOwners[t.ID], t.Title),
				TaskID:    t.ID,
				CreatedAt: now,
				Metadata:  map[string]string{"source": "claude-files", "team": t.Metadata["team"]},
			})
		}
	}

	p.lastTaskStatuses = newStatuses
	p.lastTaskOwners = newOwners
	return events
}

func mergeEvents(newEvents, oldEvents []model.Event) []model.Event {
	merged := append(newEvents, oldEvents...)
	if len(merged) > 50 {
		merged = merged[:50]
	}
	return merged
}
