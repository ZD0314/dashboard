const runtime = {
  state: {
    snapshot: null,
    events: [],
    maxEvents: 80,
    transport: 'idle',
    selectedAgentId: null,
    expandedTeams: new Set(),
  },
  connections: {
    sse: null,
    ws: null,
    wsHeartbeat: null,
  },
};

const elements = {
  agentList: document.querySelector('#agent-list'),
  agentCount: document.querySelector('#agent-count'),
  taskCard: document.querySelector('#task-card'),
  taskStatus: document.querySelector('#task-status'),
  eventStream: document.querySelector('#event-stream'),
  eventCount: document.querySelector('#event-count'),
  progressBar: document.querySelector('#progress-bar'),
  progressText: document.querySelector('#progress-text'),
  progressSummary: document.querySelector('#progress-summary'),
  updatedAt: document.querySelector('#updated-at'),
  connectionDot: document.querySelector('#connection-dot'),
  connectionText: document.querySelector('#connection-text'),
  agentTemplate: document.querySelector('#agent-item-template'),
  eventTemplate: document.querySelector('#event-item-template'),
};

boot();

async function boot() {
  setConnection('loading', '正在加载状态');
  try {
    const snapshot = await fetchInitialState();
    applySnapshot(snapshot);
    render();
    await connectRealtime();
  } catch (error) {
    console.error(error);
    setConnection('error', '初始化失败');
    elements.taskCard.innerHTML = `<div class="empty-state">无法加载 /api/state：${escapeHtml(error.message)}</div>`;
  }
}

async function fetchInitialState() {
  const response = await fetch('/api/state');
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.json();
}

async function connectRealtime() {
  const preferred = getPreferredTransport(runtime.state.snapshot);
  if (preferred === 'websocket') {
    try {
      await connectWebSocket();
      return;
    } catch (error) {
      console.warn('websocket unavailable, falling back to SSE', error);
    }
  }
  connectSSE();
}

function connectSSE() {
  cleanupConnections();
  const stream = new EventSource('/api/events');
  runtime.connections.sse = stream;

  stream.onopen = () => {
    runtime.state.transport = 'sse';
    setConnection('connected', 'SSE 实时连接中');
  };

  stream.addEventListener('snapshot', (event) => consumeEnvelope(safeJson(event.data), 'snapshot'));
  stream.addEventListener('update', (event) => consumeEnvelope(safeJson(event.data), 'update'));
  stream.addEventListener('heartbeat', () => {
    runtime.state.transport = 'sse';
    setConnection('connected', 'SSE 心跳正常');
  });
  stream.onmessage = (event) => consumeEnvelope(safeJson(event.data), 'message');
  stream.onerror = () => {
    setConnection('error', 'SSE 断开，等待重连');
  };
}

function connectWebSocket() {
  cleanupConnections();
  return new Promise((resolve, reject) => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const socket = new WebSocket(`${protocol}//${window.location.host}/ws`);
    let settled = false;
    runtime.connections.ws = socket;

    socket.onopen = () => {
      runtime.state.transport = 'websocket';
      setConnection('connected', 'WebSocket 已连接');
      runtime.connections.wsHeartbeat = window.setInterval(() => {
        if (socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify({ type: 'command', command: { id: `ping-${Date.now()}`, name: 'ping' } }));
        }
      }, 20000);
      if (!settled) {
        settled = true;
        resolve();
      }
    };

    socket.onmessage = (event) => {
      const payload = safeJson(event.data);
      if (!payload) {
        return;
      }
      if (payload.type === 'ack') {
        pushEvent({
          type: `ack.${payload.status || 'unknown'}`,
          message: payload.message || payload.name || '收到命令确认',
          timestamp: payload.timestamp || new Date().toISOString(),
        });
        renderEvents();
        return;
      }
      consumeEnvelope(payload, 'update');
    };

    socket.onerror = (error) => {
      if (!settled) {
        settled = true;
        reject(error || new Error('websocket failed'));
      } else {
        setConnection('error', 'WebSocket 异常，准备回退 SSE');
      }
    };

    socket.onclose = () => {
      clearInterval(runtime.connections.wsHeartbeat);
      runtime.connections.wsHeartbeat = null;
      if (runtime.state.transport === 'websocket') {
        setConnection('error', 'WebSocket 已关闭，回退 SSE');
        connectSSE();
      }
    };
  });
}

function cleanupConnections() {
  if (runtime.connections.sse) {
    runtime.connections.sse.close();
    runtime.connections.sse = null;
  }
  if (runtime.connections.wsHeartbeat) {
    clearInterval(runtime.connections.wsHeartbeat);
    runtime.connections.wsHeartbeat = null;
  }
  if (runtime.connections.ws) {
    const socket = runtime.connections.ws;
    runtime.connections.ws = null;
    if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
      socket.close();
    }
  }
}

function consumeEnvelope(payload, fallbackType) {
  if (!payload || typeof payload !== 'object') {
    return;
  }

  const envelope = unwrapEnvelope(payload);
  const nextState = envelope.state || envelope.snapshot || envelope.data;
  if (nextState && typeof nextState === 'object') {
    applySnapshot(nextState);
  } else if (looksLikeState(envelope)) {
    applySnapshot(envelope);
  }

  if (envelope.kind === 'heartbeat') {
    setConnection('connected', `${labelTransport(runtime.state.transport)} 心跳正常`);
    pushEvent({
      type: 'heartbeat',
      message: '状态同步',
      timestamp: envelope.timestamp || new Date().toISOString(),
    });
    render();
    return;
  }

  const eventPayload = envelope.event && typeof envelope.event === 'object' ? envelope.event : null;
  if (eventPayload) {
    pushEvent({
      type: eventPayload.type || fallbackType,
      message: eventPayload.message || '收到事件更新',
      timestamp: eventPayload.createdAt || envelope.timestamp || new Date().toISOString(),
    });
  } else if (envelope.kind === 'update') {
    pushEvent({
      type: 'sync',
      message: `状态已同步 — ${(runtime.state.snapshot?.agents || []).length} agents, ${(runtime.state.snapshot?.tasks || []).length} tasks`,
      timestamp: envelope.timestamp || new Date().toISOString(),
    });
  }

  render();
}

function applySnapshot(payload) {
  const snapshot = normalizeState(payload);
  runtime.state.snapshot = snapshot;
  // events are managed exclusively by pushEvent — do not overwrite from snapshot
  const incoming = asArray(payload.events).map((item) => ({
    type: item.type || item.level || item.kind || 'event',
    message: item.message || item.text || item.summary || JSON.stringify(item),
    timestamp: item.createdAt || item.timestamp || item.updatedAt || new Date().toISOString(),
  }));
  if (incoming.length) {
    // merge without duplicating: prepend only events not already in the list
    const existingTs = new Set(runtime.state.events.map((e) => e.timestamp + e.message));
    const fresh = incoming.filter((e) => !existingTs.has(e.timestamp + e.message));
    if (fresh.length) {
      runtime.state.events = [...fresh, ...runtime.state.events].slice(0, runtime.state.maxEvents);
    }
  }
}

function render() {
  renderAgents();
  renderTask();
  renderEvents();
  renderProgress();
}

function renderAgents() {
  const agents = runtime.state.snapshot?.agents || [];
  elements.agentCount.textContent = String(agents.length);

  if (!agents.length) {
    elements.agentList.className = 'agent-list empty-state';
    elements.agentList.innerHTML = '暂无 agent 数据';
    return;
  }

  // group by team (metadata.team or derived from id prefix)
  const teams = groupAgentsByTeam(agents);

  elements.agentList.className = 'agent-list';
  elements.agentList.innerHTML = '';

  teams.forEach(({ teamName, leader, members }) => {
    const expanded = runtime.state.expandedTeams.has(teamName);

    // leader row
    const leaderEl = buildAgentItem(leader, true, expanded, teamName);
    elements.agentList.appendChild(leaderEl);

    // member rows (only when expanded)
    if (expanded) {
      members.forEach((agent) => {
        const memberEl = buildAgentItem(agent, false, false, teamName);
        memberEl.classList.add('agent-member');
        elements.agentList.appendChild(memberEl);
      });
    }
  });
}

function groupAgentsByTeam(agents) {
  const map = new Map(); // teamName -> { leader, members[] }

  agents.forEach((agent) => {
    const teamName = agent.metadata?.team || agent.id.split('/')[0] || 'default';
    if (!map.has(teamName)) {
      map.set(teamName, { teamName, leader: null, members: [] });
    }
    const group = map.get(teamName);
    if (agent.role === 'team-lead' || agent.role === 'team_lead') {
      group.leader = agent;
    } else {
      group.members.push(agent);
    }
  });

  // if no explicit leader, use first agent
  map.forEach((group) => {
    if (!group.leader) {
      group.leader = group.members.shift() || null;
    }
  });

  return Array.from(map.values()).filter((g) => g.leader);
}

function buildAgentItem(agent, isLeader, expanded, teamName) {
  const fragment = elements.agentTemplate.content.cloneNode(true);
  const article = fragment.querySelector('.agent-item');
  article.dataset.agentId = agent.id;

  if (agent.id === runtime.state.selectedAgentId) {
    article.classList.add('selected');
    article.setAttribute('aria-pressed', 'true');
  }

  fragment.querySelector('.agent-name').textContent = agent.name;
  fragment.querySelector('.agent-role').textContent = agent.role || '未设置角色';
  fragment.querySelector('.agent-state').textContent = agent.status;
  fragment.querySelector('.agent-task').textContent = agent.currentTaskTitle || agent.currentTask || '暂无分配任务';
  fragment.querySelector('.agent-progress').textContent = `进度 ${agent.progress}%`;
  fragment.querySelector('.agent-updated').textContent = formatTime(agent.updatedAt);

  if (isLeader) {
    // show team name below role
    const roleEl = fragment.querySelector('.agent-role');
    const teamLabel = document.createElement('p');
    teamLabel.className = 'agent-team-label';
    teamLabel.textContent = `team: ${teamName}`;
    roleEl.after(teamLabel);

    // inject expand toggle into agent-topline
    const topline = fragment.querySelector('.agent-topline');
    const toggle = document.createElement('button');
    toggle.className = 'team-toggle';
    toggle.setAttribute('aria-label', expanded ? '收起成员' : '展开成员');
    toggle.textContent = expanded ? '▲' : '▼';
    toggle.addEventListener('click', (e) => {
      e.stopPropagation();
      onTeamToggle(teamName);
    });
    topline.appendChild(toggle);
  }

  article.addEventListener('click', () => onAgentClick(agent.id));
  article.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') onAgentClick(agent.id); });

  fragment.removeChild(article);
  return article;
}

function renderTask() {
  const selectedId = runtime.state.selectedAgentId;
  const taskHeading = document.querySelector('#task-heading');
  const taskSubtitle = document.querySelector('#task-subtitle');

  if (selectedId) {
    const agent = (runtime.state.snapshot?.agents || []).find((a) => a.id === selectedId);
    const agentTasks = (runtime.state.snapshot?.tasks || []).filter(
      (t) => t.agents && t.agents.includes(selectedId)
    );

    taskHeading.textContent = agent ? `${agent.name} 的任务` : '选中 Agent 的任务';
    taskSubtitle.textContent = `共 ${agentTasks.length} 个任务 — 点击空白处取消选中`;

    if (!agentTasks.length) {
      elements.taskStatus.textContent = '无任务';
      elements.taskCard.className = 'task-card empty-state';
      elements.taskCard.innerHTML = '该 Agent 暂无分配任务';
      return;
    }

    elements.taskStatus.textContent = `${agentTasks.filter((t) => t.status === 'running').length} 进行中`;
    elements.taskCard.className = 'task-card';
    elements.taskCard.innerHTML = agentTasks.map((task) => `
      <div class="task-list-item">
        <strong>${escapeHtml(task.title)}</strong>
        <span class="task-label">${escapeHtml(task.description || '暂无描述')}</span>
        <div style="margin-top:8px;display:flex;gap:8px;flex-wrap:wrap;align-items:center">
          <span class="chip">${escapeHtml(task.status)}</span>
          <span class="task-label">进度 ${task.progress}%</span>
        </div>
      </div>
    `).join('');
    return;
  }

  // default: global view
  taskHeading.textContent = '当前任务';
  taskSubtitle.textContent = '未选中 agent 时显示全局任务视图';

  const task = pickCurrentTask(runtime.state.snapshot);
  if (!task) {
    elements.taskStatus.textContent = '等待中';
    elements.taskCard.className = 'task-card empty-state';
    elements.taskCard.innerHTML = '暂无任务数据';
    return;
  }

  elements.taskStatus.textContent = task.status;
  elements.taskCard.className = 'task-card';
  elements.taskCard.innerHTML = `
    <section class="task-main">
      <div>
        <p class="task-label">任务主题</p>
        <h3 class="task-title">${escapeHtml(task.title)}</h3>
      </div>
      <p class="task-description">${escapeHtml(task.description || '暂无描述')}</p>
      <div class="task-stats">
        <article class="stat-box">
          <span class="task-label">已完成</span>
          <strong>${task.completedSteps}</strong>
        </article>
        <article class="stat-box">
          <span class="task-label">总步骤</span>
          <strong>${task.totalSteps}</strong>
        </article>
        <article class="stat-box">
          <span class="task-label">参与 Agent</span>
          <strong>${task.agents.length}</strong>
        </article>
      </div>
    </section>
    <section>
      <p class="task-label">执行清单</p>
      <div class="task-checklist">${renderChips(task.steps)}</div>
    </section>
    <section>
      <p class="task-label">协作成员</p>
      <div class="task-agents">${renderChips(task.agents)}</div>
    </section>
  `;
}

function renderEvents() {
  const events = runtime.state.events || [];
  elements.eventCount.textContent = String(events.length);

  if (!events.length) {
    elements.eventStream.className = 'event-stream empty-state';
    elements.eventStream.innerHTML = '等待事件流连接';
    return;
  }

  elements.eventStream.className = 'event-stream';

  // incremental update: only prepend new events, remove overflow
  const existing = elements.eventStream.querySelectorAll('.event-item');
  const existingCount = existing.length;
  const newCount = events.length;

  // if list shrank or first render, rebuild fully
  if (newCount < existingCount || existingCount === 0) {
    elements.eventStream.innerHTML = '';
    events.forEach((entry) => {
      elements.eventStream.appendChild(buildEventItem(entry));
    });
    return;
  }

  // prepend only the new entries at the top
  const toAdd = newCount - existingCount;
  const ref = elements.eventStream.firstChild;
  for (let i = toAdd - 1; i >= 0; i--) {
    elements.eventStream.insertBefore(buildEventItem(events[i]), ref);
  }

  // trim overflow from the bottom
  const all = elements.eventStream.querySelectorAll('.event-item');
  for (let i = runtime.state.maxEvents; i < all.length; i++) {
    all[i].remove();
  }
}

function buildEventItem(entry) {
  const fragment = elements.eventTemplate.content.cloneNode(true);
  fragment.querySelector('.event-type').textContent = entry.type;
  fragment.querySelector('.event-time').textContent = formatTime(entry.timestamp);
  fragment.querySelector('.event-message').textContent = entry.message;
  const el = fragment.querySelector('.event-item');
  fragment.removeChild(el);
  return el;
}

function renderProgress() {
  const progress = runtime.state.snapshot?.summary?.completion ?? deriveProgress(runtime.state.snapshot);
  const summary = runtime.state.snapshot?.summary?.message || '等待状态同步';
  const updatedAt = runtime.state.snapshot?.updatedAt || runtime.state.snapshot?.summary?.updatedAt;

  elements.progressBar.style.width = `${progress}%`;
  elements.progressText.textContent = `${progress}%`;
  elements.progressSummary.textContent = summary;
  elements.updatedAt.textContent = updatedAt ? `更新时间 ${formatTime(updatedAt)}` : '未更新';
}

function normalizeState(raw) {
  const agentsRaw = asArray(raw.agents);
  const tasksRaw = asArray(raw.tasks);
  const summaryRaw = raw.summary || {};

  const agents = agentsRaw.map((agent, index) => ({
    id: agent.id || agent.name || `agent-${index + 1}`,
    name: agent.name || agent.id || `Agent ${index + 1}`,
    role: agent.role || '',
    status: agent.status || 'unknown',
    currentTaskId: agent.currentTaskId || '',
    currentTaskTitle: agent.currentTaskTitle || agent.currentTask || '',
    currentTask: agent.currentTask || agent.currentTaskTitle || '',
    progress: clampPercent(agent.progress ?? 0),
    updatedAt: agent.updatedAt || raw.updatedAt || new Date().toISOString(),
  }));

  const tasks = tasksRaw.map((task, index) => {
    const steps = asArray(task.steps).map((step) => typeof step === 'string' ? step : step.title || step.name || `步骤 ${index + 1}`);
    const owners = asArray(task.ownerAgentIds || task.agents).map((item) => String(item));
    const completedSteps = Number(task.completedSteps ?? asArray(task.steps).filter((item) => item && typeof item === 'object' && item.done).length ?? 0);
    const totalSteps = Number(task.totalSteps ?? steps.length ?? 0);
    return {
      id: task.id || `task-${index + 1}`,
      title: task.title || `Task ${index + 1}`,
      description: task.description || '',
      status: task.status || 'pending',
      progress: clampPercent(task.progress ?? 0),
      completedSteps: Number.isFinite(completedSteps) ? completedSteps : 0,
      totalSteps: Number.isFinite(totalSteps) && totalSteps > 0 ? totalSteps : steps.length,
      steps,
      agents: owners,
      updatedAt: task.updatedAt || raw.updatedAt || new Date().toISOString(),
    };
  });

  return {
    updatedAt: raw.updatedAt || new Date().toISOString(),
    agents,
    tasks,
    summary: {
      completion: clampPercent(summaryRaw.completion ?? summaryRaw.progress ?? raw.progress ?? 0),
      status: summaryRaw.status || 'idle',
      message: summaryRaw.message || summaryRaw.text || '等待状态同步',
      updatedAt: summaryRaw.updatedAt || raw.updatedAt || new Date().toISOString(),
    },
    integrations: raw.integrations || {},
  };
}

function pushEvent(entry) {
  runtime.state.events = [entry, ...runtime.state.events].slice(0, runtime.state.maxEvents);
}

function onTeamToggle(teamName) {
  if (runtime.state.expandedTeams.has(teamName)) {
    runtime.state.expandedTeams.delete(teamName);
  } else {
    runtime.state.expandedTeams.add(teamName);
  }
  render();
}

function onAgentClick(agentId) {
  runtime.state.selectedAgentId = runtime.state.selectedAgentId === agentId ? null : agentId;
  render();
}

function pickCurrentTask(snapshot) {
  const tasks = snapshot?.tasks || [];
  return tasks.find((task) => task.status === 'running') || tasks.find((task) => task.status === 'blocked') || tasks[0] || null;
}

function getPreferredTransport(snapshot) {
  const preferred = snapshot?.integrations?.websocket?.enabled ? 'websocket' : 'sse';
  return preferred;
}

function unwrapEnvelope(payload) {
  return payload && typeof payload === 'object' ? payload : {};
}

function renderChips(items) {
  if (!items.length) {
    return '<span class="chip">暂无</span>';
  }
  return items.map((item) => `<span class="chip">${escapeHtml(item)}</span>`).join('');
}

function setConnection(status, text) {
  elements.connectionDot.className = `status-dot${status === 'connected' ? ' connected' : ''}${status === 'error' ? ' error' : ''}`;
  elements.connectionText.textContent = text;
}

function labelTransport(transport) {
  return transport === 'websocket' ? 'WebSocket' : transport === 'sse' ? 'SSE' : '实时';
}

function deriveProgress(snapshot) {
  if (!snapshot) {
    return 0;
  }
  const tasks = snapshot.tasks || [];
  if (tasks.length) {
    const total = tasks.reduce((sum, item) => sum + clampPercent(item.progress), 0);
    return clampPercent(Math.round(total / tasks.length));
  }
  return 0;
}

function looksLikeState(value) {
  return !!(value && typeof value === 'object' && ('agents' in value || 'tasks' in value || 'summary' in value));
}

function safeJson(text) {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function clampPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(number)));
}

function formatTime(value) {
  if (!value) {
    return '未知时间';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return String(value);
  }

  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    month: '2-digit',
    day: '2-digit',
  }).format(date);
}

function escapeHtml(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}
