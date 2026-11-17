
import { createContext, useContext, useEffect, useRef, useState } from 'react';
import { applyTaskUpdate } from './taskstate';

export type BotDraft = { name: string; instructions: string };
export type BotProfile = BotDraft & { id: string; avatarSlot: number };
const botPath = (id: string) => `/api/bots/${encodeURIComponent(id)}`;

export type APIChannel = {
  id: string; name: string; base_url: string; enabled: boolean; auth_required: boolean;
  api_key?: string; has_api_key: boolean; models: string[]; hidden_models: string[];
};
export type APISettings = {
  base_url: string;
  model: string;
  agent_provider_id?: string;
  agent_model?: string;
  api_key?: string;
  has_api_key: boolean;
  providers?: APIChannel[];
  default_provider_id?: string;
  provider_id?: string;
  env_override?: boolean;
  env_model?: string;
  env_agent_model?: string;
};

// enabled 恒为 true：所有环境都需要密码登录。
export type AuthInfo = { enabled: boolean; authenticated?: boolean; mode?: 'bearer' | 'session' | 'none' };

export type UpdateStatus = {
  current: string;
  latest?: string;
  release_url: string;
  state: 'no_release' | 'unversioned' | 'current' | 'available';
};

// ---------- 类型（与 Go 后端的事件一一对应） ----------

export type Card = { id: string; question: string; options: string[]; status: 'pending' | 'answered' | 'dismissed'; answer?: string };
export type SecretReq = { id: string; name: string; description?: string; status: 'pending' | 'saved' | 'cancelled'; exists?: boolean };
export type Attachment = { path: string; name: string; size: number; mime?: string };
export type Reaction = { by: 'user' | 'bot'; emoji: string };
export type Msg = {
  id: string; role: 'user' | 'assistant' | 'notice'; text: string; card?: Card; secret?: SecretReq; tools?: string[]; created_at: string;
  thinking?: string; thinking_ms?: number; files?: Attachment[]; reactions?: Reaction[]; hidden?: boolean;
  ok?: boolean; // 仅 command.result 合成的 notice 行：false 表示命令执行失败
};
export type Step = { id: string; tool: string; args: Record<string, unknown>; thought: string; output?: string; error?: boolean; denied?: boolean; done: boolean; at: number; doneAt?: number };
export type TaskStatus = 'queued' | 'running' | 'paused' | 'completed' | 'failed' | 'cancelled';
export type Task = {
  id: string; title: string; input: string; status: TaskStatus; steps: Step[]; text: string; thinking?: string;
  executor?: string; // 空 = builtin；外部任务写 CLI kind（codex/pi/omp）
  output?: string; error?: string; stop_reason?: string; created: number; started?: number; ended?: number; awaiting: number;
  prio: number; pausing?: boolean; files: string[]; summary?: string; duration?: number;
  lastEvent: number; // 最近一次真正的进展（不含心跳）
  lastBeat: number;  // 最近一次心跳：证明进程还活着
  activeMs: number;  // 之前几段运行累计的时长（暂停/重启后继续时累加）
};
export type Approval = {
  id: string; task_id: string; tool_name: string; args: Record<string, unknown>; risk: string; reason: string;
  status: 'pending' | 'allow' | 'deny' | 'expired'; scope?: string; created: number;
};
export type Item = { type: 'msg' | 'task' | 'approval'; id: string };
export type Info = { name: string; chat_model: string; agent_model: string; agent_cli?: string; agent_cli_model?: string; agent_cli_auth?: CLIAuth; policy: string; demo: boolean; max_concurrent?: number; stall_sec?: number; timezone?: string; version?: string };
// 每个 Bot 的聊天 / Agent 各用哪个渠道（API 渠道页标注用途）。
export type ChannelUse = { bot_id: string; name: string; chat_provider_id?: string; agent_provider_id?: string; agent_cli?: string };
export type ChannelsView = { providers: APIChannel[]; usage: ChannelUse[] };
export type BotRoutes = Pick<APISettings, 'default_provider_id' | 'model' | 'agent_provider_id' | 'agent_model'>;
export type CLIAuth = 'subscription' | 'env' | 'channel';
export type CLIInstall = { state: 'running' | 'done' | 'failed'; step?: string; error?: string; log?: string; version?: string; started_at: string; finished_at?: string };
export type AgentCLI = {
  id: string; name: string; installed: boolean;
  subscription?: string; login?: string; model_option?: boolean; models?: string[];
  // path：检测到的可执行文件；managed：由 reeve 一键安装；installable：可以一键安装/更新
  // version/available：`<exe> --version` 探测出的版本与是否跑得起来；note：不可用时说明原因
  // probed：这次是否真的探测过；false 时 available 没有结论（不能当成不可用）
  path?: string; managed?: boolean; installable?: boolean; install?: CLIInstall;
  version?: string; probed?: boolean; available?: boolean; note?: string;
};
// cli 为空 = 内置 Agent（用 Agent API 模型）；否则用该 CLI 在本机登录的账户（官方订阅）。
// Pi 用 reeve 的 API 渠道：cli_channel + cli_model（经本机网关转发，Pi 拿不到真实 Key）。
export type AgentConnection = { cli: string; cli_model?: string; cli_auth?: CLIAuth; cli_channel?: string };
export type AgentConnections = AgentConnection & { clis: AgentCLI[] };
// 每个 Bot 的执行器白名单与默认执行器；allowed 必须包含 builtin，default 必须在 allowed 内。
export type ExecutorOptions = { cli_model?: string; cli_auth?: CLIAuth; cli_channel?: string };
export type Executors = { allowed: string[]; default: string; clis?: Record<string, ExecutorOptions> };

// ---------- 环境变量、斜线命令、技能索引 ----------

// env 变量是 name → value 的平表；实例级所有 Bot 共用，bot 级只影响一个 Bot。
export type EnvVars = Record<string, string>;
export type EnvSaveResult = { warnings: string[] };
// get/save 都走整体替换：保存时先合并当前表，再 PUT 完整对象。
export type EnvAPI = {
  get: (signal?: AbortSignal) => Promise<EnvVars>;
  save: (vars: EnvVars) => Promise<EnvSaveResult>;
};

export type CommandInfo = { name: string; aliases: string[]; summary: string; usage: string };

export type SkillInfo = { name: string; description: string; when_to_use: string; path: string };
export type SkillIndex = { skills: SkillInfo[]; errors: string[] };

export type State = {
  items: Item[]; msgs: Record<string, Msg>; tasks: Record<string, Task>; approvals: Record<string, Approval>;
  streaming: string; toolLabel: string; busy: boolean;
  thinking: string; thinkStart: number; thinkEnd: number; // 聊天层这一轮的思考（流式，临时）
  ready: boolean; connected: boolean; lastSeq: number;
  news: number; // 实时到达的"值得提醒"的事件计数（新回复、任务结束、等你确认），用于标签页未读和通知
  lastNews: string;
};

type Wire = { seq: number; kind: string; time: string; data: any };
type AgentEv = { task_id: string; type: string; time: string; data: any };

const empty = (): State => ({ items: [], msgs: {}, tasks: {}, approvals: {}, streaming: '', toolLabel: '', busy: false, thinking: '', thinkStart: 0, thinkEnd: 0, ready: false, connected: false, lastSeq: 0, news: 0, lastNews: '' });
const resetTurn = (s: State) => { s.streaming = ''; s.toolLabel = ''; s.thinking = ''; s.thinkStart = 0; s.thinkEnd = 0; };
const endThink = (s: State) => { if (s.thinkStart && !s.thinkEnd) s.thinkEnd = Date.now(); };
const ts = (s?: string) => (s ? new Date(s).getTime() : Date.now());
const news = (s: State, text: string) => { if (s.ready) { s.news++; s.lastNews = text; } };
export const terminal = (st: string) => st === 'completed' || st === 'failed' || st === 'cancelled';

// apply 把一条事件合并进状态（原地修改，外层负责触发重渲染）。
function apply(s: State, ev: Wire) {
  const d = ev.data;
  switch (ev.kind) {
    case 'replay.done': s.ready = true; s.busy = !!d.busy; break;
    case 'message': {
      if (d.role === 'notice') break; // 系统通知只给聊天层看
      if (d.hidden) { s.items = s.items.filter((it) => it.id !== d.id); delete s.msgs[d.id]; break; }
      if (!s.msgs[d.id]) {
        // 聊天层的回应（例如"开始做了"）排在它这一轮刚派出的工作卡片前面，读起来更顺
        let at = s.items.length;
        if (d.role === 'assistant') while (at > 0 && s.items[at - 1].type === 'task' && s.tasks[s.items[at - 1].id]?.steps.length === 0 && s.tasks[s.items[at - 1].id]?.created > ts(d.created_at) - 60_000) at--;
        s.items.splice(at, 0, { type: 'msg', id: d.id });
        if (d.role === 'assistant' && d.text) news(s, d.text);
      }
      s.msgs[d.id] = d;
      if (d.role === 'assistant' && (d.text || d.thinking)) resetTurn(s);
      break;
    }
    case 'message.hidden': {
      const ids = new Set<string>(d || []);
      s.items = s.items.filter((it) => !(it.type === 'msg' && ids.has(it.id)));
      ids.forEach((id) => delete s.msgs[id]);
      break;
    }
    case 'message.reaction': {
      const m = s.msgs[d.id];
      if (!m) break;
      const keep = (m.reactions || []).filter((r) => r.by !== d.by); // 每一方一条，切换
      m.reactions = d.on ? [...keep, { by: d.by, emoji: d.emoji }] : keep;
      break;
    }
    case 'card':
      for (const m of Object.values(s.msgs)) if (m.card?.id === d.id) m.card = d;
      break;
    case 'secret.card':
      for (const m of Object.values(s.msgs)) if (m.secret?.id === d.id) m.secret = d;
      break;
    case 'chat.thinking':
      if (!s.thinkStart) s.thinkStart = Date.now();
      s.thinkEnd = 0; s.thinking += d.text;
      break;
    case 'chat.delta': endThink(s); s.streaming += d.text; break;
    case 'chat.status': s.busy = d.busy; if (!d.busy) resetTurn(s); break;
    case 'chat.tool': endThink(s); s.toolLabel = d.label; break;
    case 'task.created': {
      if (!s.tasks[d.id]) s.items.push({ type: 'task', id: d.id });
      const c = ts(d.created_at);
      s.tasks[d.id] = { id: d.id, title: d.title, input: d.input, executor: d.executor || undefined, status: d.status, steps: [], text: '', created: c, awaiting: 0, prio: d.prio || c * 1e6, stop_reason: d.stop_reason, files: [], lastEvent: c, lastBeat: c, activeMs: 0 };
      break;
    }
    case 'task.updated': {
      const t = s.tasks[d.id];
      if (!t) break;
      applyTaskUpdate(t, d, ts(ev.time));
      break;
    }
    case 'task.pausing': { const t = s.tasks[d.id]; if (t) { t.pausing = true; t.stop_reason = d.stop_reason; } break; }
    case 'task.prio': { const t = s.tasks[d.id]; if (t) t.prio = d.prio; break; }
    case 'task.summary': {
      const t = s.tasks[d.id];
      if (t) { t.summary = d.summary; if (d.files?.length) t.files = d.files; t.duration = d.duration_sec * 1000; }
      break;
    }
    case 'agent': applyAgent(s, d as AgentEv, ev.seq > 0); break;
    case 'command.result': {
      // 斜线命令的结果只进聊天流：合成一条居中的 notice 行，不碰 busy，也不算「新消息」。
      const id = `cmd_${ev.seq}`;
      if (!s.msgs[id]) {
        s.msgs[id] = { id, role: 'notice', text: typeof d.text === 'string' ? d.text : '', ok: d.ok !== false, created_at: ev.time };
        s.items.push({ type: 'msg', id });
      } else {
        s.msgs[id].text = typeof d.text === 'string' ? d.text : s.msgs[id].text;
        s.msgs[id].ok = d.ok !== false;
      }
      break;
    }
  }
}

function applyAgent(s: State, e: AgentEv, persisted: boolean) {
  const t = s.tasks[e.task_id]; // 聊天层直接发起的审批（task_id 以 chat_ 开头）没有对应任务
  const d = e.data || {};
  const at = ts(e.time);
  if (e.type === 'approval.requested') {
    if (!s.approvals[d.id]) { s.items.push({ type: 'approval', id: d.id }); news(s, '有一个操作等你确认'); }
    s.approvals[d.id] = { id: d.id, task_id: e.task_id, tool_name: d.tool_name, args: d.args || {}, risk: d.risk, reason: d.reason, status: 'pending', created: at };
    if (t) { t.awaiting++; t.lastEvent = at; }
    return;
  }
  if (e.type === 'approval.decided') {
    const a = s.approvals[d.approval_id];
    if (a && a.status === 'pending') { a.status = d.decision; a.scope = d.scope; if (t) { t.awaiting = Math.max(0, t.awaiting - 1); t.lastEvent = at; } }
    return;
  }
  if (!t) return;
  if (e.type === 'heartbeat') { t.lastBeat = at; return; }
  if (persisted || e.type !== 'thinking.delta') t.lastEvent = Math.max(t.lastEvent, at);
  else t.lastEvent = Math.max(t.lastEvent, at); // 思考片段也算"有动静"
  switch (e.type) {
    case 'task.started':
      t.status = 'running'; t.stop_reason = undefined; t.pausing = false;
      t.started = at; t.ended = undefined;
      break;
    case 'thinking.delta': t.thinking = ((t.thinking || '') + d.text).slice(-600); break;
    case 'text.delta': t.text = (t.text + d.text).slice(-4000); t.thinking = ''; break;
    case 'tool.call':
      t.thinking = '';
      // 外部 CLI 的文字是它的实时输出：一直留在 t.text 里给输出区显示，不塞进工具步骤。
      t.steps.push({ id: d.call_id, tool: d.tool, args: d.args || {}, thought: t.executor ? '' : t.text.trim(), done: false, at });
      if (!t.executor) t.text = '';
      break;
    case 'tool.result': {
      const st = t.steps.find((x) => x.id === d.call_id && !x.done) || t.steps[t.steps.length - 1];
      if (st) {
        Object.assign(st, { output: d.output, error: d.error, denied: d.denied, done: true, doneAt: at });
        const p = (st.args as { path?: string }).path;
        if ((st.tool === 'write_file' || st.tool === 'edit_file') && p && !d.error && !d.denied && !t.files.includes(p)) t.files.push(p);
      }
      break;
    }
    case 'task.paused':
      if (t.started) t.activeMs += at - t.started;
      t.status = 'paused'; t.pausing = false; t.text = ''; t.thinking = ''; t.started = undefined;
      for (const st of t.steps) if (!st.done) st.done = true;
      break;
    case 'task.completed': case 'task.failed': case 'task.cancelled':
      if (t.started) t.activeMs += at - t.started;
      t.status = d.status; t.output = d.output; t.ended = at; t.started = undefined; t.pausing = false;
      if (d.error) t.error = d.error;
      t.text = ''; t.thinking = '';
      for (const st of t.steps) if (!st.done) st.done = true;
      news(s, `任务「${t.title}」${d.status === 'completed' ? '完成了' : d.status === 'failed' ? '失败了' : '已停止'}`);
      break;
  }
}

// useBot 连接 SSE，维护整个界面的状态。断线后带 Last-Event-ID（或 ?after=）重连补齐。
// The caller keys the chat view by Bot ID, so replay and live events start from a clean state.
export function useBot(id: string) {
  const ref = useRef<State>(empty());
  const [, setTick] = useState(0);
  useEffect(() => {
    let es: EventSource | null = null;
    let raf = 0, retry = 0, closed = false;
    const render = () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; setTick((n) => n + 1); }); };
    const connect = () => {
      const s = ref.current;
      es = new EventSource(`${botPath(id)}/stream${s.lastSeq ? `?after=${s.lastSeq}` : ''}`);
      es.onopen = () => { s.connected = true; s.busy = false; resetTurn(s); render(); };
      es.onmessage = (m) => {
        const ev = JSON.parse(m.data) as Wire;
        if (ev.seq > 0) { if (ev.seq <= s.lastSeq) return; s.lastSeq = ev.seq; }
        apply(s, ev);
        render();
      };
      es.onerror = () => {
        s.connected = false; render();
        // EventSource 看不到状态码：断线时问一下是不是登录失效了（服务器模式）。
        fetch('/api/auth/me').then((r) => r.json()).then((me: AuthInfo) => {
          if (me.enabled && !me.authenticated) location.assign('/login');
        }).catch(() => {});
        if (es?.readyState === EventSource.CLOSED && !closed) { // 浏览器放弃了，自己重连
          es.close();
          retry = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => { closed = true; es?.close(); clearTimeout(retry); cancelAnimationFrame(raf); };
  }, [id]);
  return ref.current;
}

// 服务器模式下登录过期时，接口返回 401：直接回到登录页。
function checkAuth(r: Response) {
  if (r.status === 401 && location.pathname !== '/login') location.assign('/login');
}

async function post(path: string, body: unknown = {}, method = 'POST') {
  const r = await fetch(path, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  checkAuth(r);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, { signal });
  checkAuth(r);
  const data = await r.json().catch(() => null);
  if (!r.ok || data === null) throw new Error(data?.error || '无法读取数据，请确认 reeve 后端已启动');
  return data as T;
}

// 实例级（/api/env）与 bot 级（/api/bots/{id}/env）语义相同，只是 base 不同。
function envAPI(base: string): EnvAPI {
  return {
    get: async (signal?: AbortSignal): Promise<EnvVars> => {
      const v = await get<{ vars?: EnvVars }>(`${base}/env`, signal);
      return v.vars ?? {};
    },
    save: async (vars: EnvVars): Promise<EnvSaveResult> => {
      const r = await post(`${base}/env`, { vars }, 'PUT') as { warnings?: string[] };
      return { warnings: r.warnings ?? [] };
    },
  };
}

function skillIndex(raw: SkillIndex): SkillIndex {
  return { skills: raw.skills ?? [], errors: raw.errors ?? [] };
}

export type TaskAction = 'pause' | 'resume' | 'cancel' | 'retry' | 'top' | 'up';

export type BotAPI = {
  info: (signal?: AbortSignal) => Promise<Info>;
  send: (text: string, files?: Attachment[]) => Promise<unknown>;
  stop: () => Promise<unknown>;
  stopReply: () => Promise<unknown>;
  regenerate: () => Promise<unknown>;
  edit: (id: string, text: string) => Promise<unknown>;
  react: (id: string, emoji: string) => Promise<unknown>;
  task: (id: string, action: TaskAction) => Promise<unknown>;
  upload: (f: File) => Promise<Attachment>;
  fileURL: (path: string, download?: boolean) => string;
  // 批准或拒绝一次工具调用；只能由用户点按钮触发。
  decide: (id: string, decision: 'once' | 'session' | 'deny') => Promise<unknown>;
  answer: (id: string, answer: string) => Promise<unknown>;
  dismiss: (id: string) => Promise<unknown>;
  saveSecret: (requestId: string, value: string) => Promise<unknown>;
  cancelSecret: (requestId: string) => Promise<unknown>;
  // 所有 Bot 共用的命令定义（输入框的 / 候选）。
  commands: (signal?: AbortSignal) => Promise<CommandInfo[]>;
};

export function createBotAPI(botID: string): BotAPI {
  const base = botPath(botID);
  return {
    info: (signal?: AbortSignal): Promise<Info> => get(`${base}/info`, signal),
    send: (text: string, files: Attachment[] = []) => post(`${base}/messages`, { text, files }),
    stop: () => post(`${base}/stop`),
    stopReply: () => post(`${base}/stop?scope=reply`),
    regenerate: () => post(`${base}/regenerate`),
    edit: (id: string, text: string) => post(`${base}/messages/${id}/edit`, { text }),
    react: (id: string, emoji: string) => post(`${base}/messages/${id}/react`, { emoji }),
    task: (id: string, action: TaskAction) => post(`${base}/tasks/${id}/${action}`),
    upload: async (f: File): Promise<Attachment> => {
      const fd = new FormData();
      fd.append('file', f, f.name || 'pasted.png');
      const r = await fetch(`${base}/uploads`, { method: 'POST', body: fd });
      checkAuth(r);
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
      const out = (await r.json()) as Attachment[];
      return out[0];
    },
    fileURL: (path: string, download = false) => `${base}/files?path=${encodeURIComponent(path)}${download ? '&download=1' : ''}`,
    // 批准或拒绝一次工具调用；只能由用户点按钮触发。
    decide: (id: string, decision: 'once' | 'session' | 'deny') => post(`${base}/approvals/${id}`, { decision }),
    answer: (id: string, answer: string) => post(`${base}/cards/${id}`, { answer }),
    dismiss: (id: string) => post(`${base}/cards/${id}`, { dismiss: true }),
    // 密钥值只走这个接口：直接加密落盘，不进聊天记录、事件流或模型上下文
    saveSecret: (requestId: string, value: string) => post(`${base}/secrets`, { request_id: requestId, value }),
    cancelSecret: (requestId: string) => post(`${base}/secrets/requests/${requestId}/cancel`),
    commands: async (signal?: AbortSignal): Promise<CommandInfo[]> => (await get<{ commands?: CommandInfo[] }>('/api/commands', signal)).commands ?? [],
  };
}

export const BotContext = createContext<{ bot: BotProfile; api: BotAPI } | null>(null);
export function useActiveBot() {
  const context = useContext(BotContext);
  if (!context) throw new Error('Missing active Bot');
  return context;
}

// 设置页用到的接口：本 Bot 的模型路由与 Agent 连接、CLI 安装，以及所有 Bot 共用的 API 渠道。
export type SettingsAPI = {
  // probe=false：只刷新安装状态与已缓存的版本（安装轮询用，不触发 --version 探测）。
  connections: (signal?: AbortSignal, probe?: boolean) => Promise<AgentConnections>;
  executors: (signal?: AbortSignal) => Promise<Executors>;
  saveExecutors: (e: Executors) => Promise<Executors>;
  // 复制来源 Bot 的聊天模型、Agent 模型和执行方式。
  copySettings: (from: string) => Promise<APISettings>;
  // 一键安装 / 更新 CLI（装到数据目录）。
  installCLI: (id: string) => Promise<AgentConnections>;
  settings: (signal?: AbortSignal) => Promise<APISettings>;
  saveSettings: (s: APISettings) => Promise<APISettings>;
  // 本 Bot 用哪个渠道的哪个模型（不改渠道）。
  saveRoutes: (r: BotRoutes) => Promise<APISettings>;
  // 所有 Bot 共用的 API 渠道。
  channels: (signal?: AbortSignal) => Promise<ChannelsView>;
  saveChannels: (providers: APIChannel[]) => Promise<ChannelsView>;
  providerModels: (s: APISettings) => Promise<{ models: string[] }>;
  testProvider: (s: APISettings) => Promise<{ latency_ms: number }>;
  checkUpdate: (signal?: AbortSignal) => Promise<UpdateStatus>;
  version: (signal?: AbortSignal) => Promise<string | undefined>;
  // 设置页的「技能」面板：作用域跟随当前选中的 Bot。
  skills: (signal?: AbortSignal) => Promise<SkillIndex>;
};

export function createSettingsAPI(bot = 'main'): SettingsAPI {
  const base = botPath(bot);
  return {
    connections: (signal?: AbortSignal, probe = true): Promise<AgentConnections> => get(`${base}/settings/connections${probe ? '' : '?probe=0'}`, signal),
    executors: (signal?: AbortSignal): Promise<Executors> => get(`${base}/executors`, signal),
    saveExecutors: (e: Executors): Promise<Executors> => post(`${base}/executors`, e, 'PUT'),
    copySettings: (from: string): Promise<APISettings> => post(`${base}/settings/copy`, { from }),
    installCLI: (id: string): Promise<AgentConnections> => post('/api/settings/clis/install', { id }),
    settings: async (signal?: AbortSignal): Promise<APISettings> => {
      const settings = await get<APISettings>(`${base}/settings`, signal);
      if (typeof settings.base_url !== 'string' || typeof settings.model !== 'string') {
        throw new Error('后端版本与网页不匹配，请升级后重试');
      }
      return settings;
    },
    saveSettings: (s: APISettings): Promise<APISettings> => post(`${base}/settings`, s),
    saveRoutes: (r: BotRoutes): Promise<APISettings> => post(`${base}/settings/routes`, r),
    channels: async (signal?: AbortSignal): Promise<ChannelsView> => {
      const v = await get<ChannelsView>('/api/channels', signal);
      if (!Array.isArray(v.providers)) throw new Error('后端版本与网页不匹配，请升级后重试');
      return v;
    },
    saveChannels: (providers: APIChannel[]): Promise<ChannelsView> => post('/api/channels', { providers }),
    providerModels: (s: APISettings): Promise<{ models: string[] }> => post('/api/channels/models', s),
    testProvider: (s: APISettings): Promise<{ latency_ms: number }> => post('/api/channels/test', s),
    checkUpdate: (signal?: AbortSignal): Promise<UpdateStatus> => get('/api/update', signal),
    version: async (signal?: AbortSignal) => (await get<Info>(`${botPath('main')}/info`, signal)).version,
    skills: async (signal?: AbortSignal): Promise<SkillIndex> => skillIndex(await get<SkillIndex>(`${base}/skills`, signal)),
  };
}

export const api = {
  // 实例级环境变量：所有 Bot 共用同一份。
  env: envAPI('/api'),
  bots: (signal?: AbortSignal): Promise<BotProfile[]> => get('/api/bots', signal),
  createBot: (draft: BotDraft): Promise<BotProfile> => post('/api/bots', draft),
  editBot: (id: string, draft: BotDraft): Promise<BotProfile> => post(botPath(id), draft, 'PATCH'),
  deleteBot: (id: string, purge = false): Promise<{ ok: boolean }> => post(`${botPath(id)}${purge ? '?purge=1' : ''}`, {}, 'DELETE'),
  copyBotSettings: (to: string, from: string): Promise<APISettings> => post(`${botPath(to)}/settings/copy`, { from }),
  botPaths: (id: string): Promise<{ data_dir: string; workspace: string }> => get(`${botPath(id)}/paths`),
  me: (): Promise<AuthInfo> => get('/api/auth/me'),
  // 修改密码：成功后其他设备上的会话立即失效，当前浏览器会收到新 Cookie。
  changePassword: (current: string, next: string): Promise<unknown> =>
    post('/api/auth/password', { current_password: current, new_password: next }),
  // 退出登录：后端清 Cookie 后返回 204，没有 JSON，所以直接用 fetch 判断状态码。
  logout: async (): Promise<void> => {
    const r = await fetch('/api/auth/logout', { method: 'POST' });
    checkAuth(r);
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || '退出登录失败，请重试');
  },
};

export const isActive = (t: Task) => t.status === 'queued' || t.status === 'running';
export const isOpen = (t: Task) => !terminal(t.status);
export const isImage = (name: string, mime?: string) => (mime ? mime.startsWith('image/') && !mime.includes('svg') : /\.(png|jpe?g|gif|webp)$/i.test(name));
