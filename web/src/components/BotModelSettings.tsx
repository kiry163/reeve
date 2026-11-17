import { BadgeCheck, Bot, Check, CircleAlert, Copy, Download, Loader2, MessageSquare, Plug, RefreshCw, Save, Terminal, Undo2, Workflow } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { AgentCLI, AgentConnections, APIChannel, APISettings, BotProfile, CLIAuth, ExecutorOptions, Executors, SettingsAPI } from '../api';
import { availabilityLabel, execSignature, executorUnavailable } from '../executorstate';
import { BotAvatar } from './BotAvatar';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';
const visible = (p: APIChannel) => p.models.filter(m => !p.hidden_models.includes(m));
type Notice = { error: boolean; text: string };
type AgentMode = 'api' | 'cli';

/**
 * 一个 Bot 的模型设置：聊天模型和 Agent（内置执行器或白名单里的外部 CLI）。API 渠道是所有 Bot 共用的，
 * 在「API 渠道」页管理；这里只选每个 Bot 用哪个渠道的哪个模型、以及能把任务交给哪些执行器。
 */
export default function BotModelSettings({ target, bot, bots, saving, setSaving, onDirty, onSelectBot, onOpenChannels, onSaved }: {
  target: SettingsAPI;
  bot: BotProfile;
  bots: BotProfile[];
  saving: boolean;
  setSaving: (v: boolean) => void;
  onDirty: (dirty: boolean) => void;
  onSelectBot: (id: string) => void;
  onOpenChannels: () => void;
  onSaved: () => void;
}) {
  const [state, setState] = useState<APISettings>();
  const [channels, setChannels] = useState<APIChannel[]>([]);
  const [defaultID, setDefaultID] = useState('');
  const [model, setModel] = useState('');
  const [agentProviderID, setAgentProviderID] = useState('');
  const [agentModel, setAgentModel] = useState('');
  const [settingsDirty, setSettingsDirty] = useState(false);
  const [conn, setConn] = useState<AgentConnections>();
  const [exec, setExec] = useState<Executors>();
  const [allowed, setAllowed] = useState<string[]>([]);
  const [execDefault, setExecDefault] = useState('builtin');
  const [options, setOptions] = useState<Record<string, ExecutorOptions>>({});
  const [optID, setOptID] = useState('');
  const [detecting, setDetecting] = useState(false);
  const [copyFrom, setCopyFrom] = useState('');
  const [work, setWork] = useState('');
  const [notice, setNotice] = useState<Notice>();
  const [attempt, setAttempt] = useState(0);

  function applySettings(s: APISettings) {
    const rows = (s.providers ?? []).map(p => ({ ...p, models: p.models ?? [], hidden_models: p.hidden_models ?? [] }));
    setState(s); setChannels(rows); setDefaultID(s.default_provider_id ?? '');
    setModel(s.model); setAgentProviderID(s.agent_provider_id || s.default_provider_id || '');
    setAgentModel(s.agent_model || s.model); setSettingsDirty(false);
  }
  function applyExecutors(e: Executors) {
    const ids = [...new Set(e.allowed ?? [])].filter(Boolean);
    if (!ids.includes('builtin')) ids.unshift('builtin');
    const clis = e.clis ?? {};
    const def = ids.includes(e.default) ? e.default : 'builtin';
    setExec({ ...e, allowed: ids, default: def, clis });
    setAllowed(ids); setExecDefault(def); setOptions(clis);
    setOptID(def !== 'builtin' ? def : ids.find(id => id !== 'builtin') ?? '');
  }
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    Promise.all([target.settings(signal), target.connections(signal), target.executors(signal)]).then(([s, c, e]) => {
      if (signal.aborted) return;
      if (!Array.isArray(c.clis) || !Array.isArray(e.allowed)) throw new Error('请更新并重启 reeve 后端后重试');
      applySettings(s); applyExecutors(e); setConn(c);
    }).catch(e => { if (!signal.aborted) setNotice({ error: true, text: message(e) }); });
    return () => controller.abort();
  }, [attempt, target]);

  const execDirty = !!exec && execSignature(allowed, execDefault, options) !== execSignature(exec.allowed, exec.default, exec.clis ?? {});
  const dirty = settingsDirty || execDirty;
  useEffect(() => { onDirty(dirty); }, [dirty]);

  const mode: AgentMode = execDefault === 'builtin' ? 'api' : 'cli';
  const route = channels.find(p => p.id === defaultID);
  const agentRoute = channels.find(p => p.id === agentProviderID);
  const busy = saving || !!work;
  const chatSet = !!defaultID || !!model;
  const chatValid = !chatSet || (!!route?.enabled && visible(route).includes(model));
  const agentSet = mode === 'api' && (!!agentProviderID || !!agentModel);
  const agentValid = !agentSet || (!!agentRoute?.enabled && visible(agentRoute).includes(agentModel));
  const allowedExternal = allowed.filter(id => id !== 'builtin');
  const activeOptID = allowedExternal.includes(optID) ? optID : allowedExternal[0] ?? '';
  const editingCLI = conn?.clis.find(c => c.id === activeOptID);
  const editingOpt = options[activeOptID] ?? {};
  const editingAuth: CLIAuth = activeOptID === 'pi' ? 'channel' : editingOpt.cli_auth ?? 'subscription';
  const defaultCLI = conn?.clis.find(c => c.id === execDefault);
  const piRoute = channels.find(p => p.id === (options['pi']?.cli_channel ?? ''));
  const piValid = execDefault !== 'pi' || (!!piRoute?.enabled && visible(piRoute).includes(options['pi']?.cli_model ?? ''));
  const cliValid = execDefault === 'builtin' || (!!defaultCLI?.installed && !executorUnavailable(defaultCLI) && piValid);
  const valid = chatValid && agentValid && cliValid;
  const invalidReason = !chatValid ? '聊天：请选择已启用渠道里的模型'
    : !agentValid ? 'Agent：请选择已启用渠道里的模型'
    : !cliValid ? (!defaultCLI?.installed ? `默认执行器「${defaultCLI?.name ?? execDefault}」还没装好`
      : executorUnavailable(defaultCLI) ? `默认执行器「${defaultCLI.name}」不可用：${defaultCLI.note ?? '--version 探测失败'}`
      : 'Pi：请选择已启用渠道里的模型')
    : '';
  const others = bots.filter(b => b.id !== bot.id);
  const executorNames: Record<string, string> = { builtin: '内置执行器' };
  conn?.clis.forEach(c => { executorNames[c.id] = c.name; });
  const externalChecked = allowedExternal.length > 0;

  function changed() { setSettingsDirty(true); setNotice(undefined); }

  function toggleExecutor(id: string, on: boolean) {
    const next = on ? [...allowed, id] : allowed.filter(x => x !== id);
    if (!next.includes('builtin')) next.unshift('builtin');
    setAllowed(next);
    if (!next.includes(execDefault)) setExecDefault('builtin');
    if (on) setOptID(id);
    setNotice(undefined);
  }
  function pickDefault(id: string) {
    setExecDefault(id);
    if (id !== 'builtin') setOptID(id);
    setNotice(undefined);
  }
  function setOption(id: string, patch: ExecutorOptions) {
    setOptions(prev => ({ ...prev, [id]: { ...prev[id], ...patch } }));
    setNotice(undefined);
  }
  // 提交草稿：只带白名单内的执行器选项；取消勾选时保留它已保存的选项，重新勾选不用重填。
  function draftExecutors(): Executors {
    const clis: Record<string, ExecutorOptions> = {};
    for (const id of allowed) {
      if (id === 'builtin') continue;
      const o = options[id] ?? {};
      clis[id] = id === 'pi'
        ? { cli_model: (o.cli_model ?? '').trim(), cli_auth: 'channel', cli_channel: o.cli_channel ?? '' }
        : { cli_model: (o.cli_model ?? '').trim(), cli_auth: o.cli_auth ?? 'subscription' };
    }
    for (const [id, o] of Object.entries(options)) if (!clis[id] && !allowed.includes(id)) clis[id] = o;
    return { allowed, default: execDefault, clis };
  }

  async function save() {
    if (!state || !valid || !exec) return;
    setSaving(true); setNotice(undefined);
    try {
      if (settingsDirty) {
        applySettings(await target.saveRoutes({ default_provider_id: defaultID, model, agent_provider_id: agentProviderID, agent_model: agentModel }));
      }
      if (execDirty) {
        applyExecutors(await target.saveExecutors(draftExecutors()));
        // 元信息（安装状态、版本）不需要重新探测；保存后刷新一次缓存状态就够了。
        target.connections(undefined, false).then(c => setConn(c)).catch(() => {});
      }
      setNotice({ error: false, text: `「${bot.name}」的设置已保存，下一条消息和新任务使用新配置。` });
      onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setSaving(false); }
  }
  function discard() {
    setNotice(undefined); setAttempt(n => n + 1);
  }
  async function copySettings() {
    if (!copyFrom) return;
    setWork('copy'); setSaving(true); setNotice(undefined);
    try {
      applySettings(await target.copySettings(copyFrom));
      applyExecutors(await target.executors());
      setConn(await target.connections(undefined, false));
      setNotice({ error: false, text: `已复制「${bots.find(b => b.id === copyFrom)?.name}」的模型、执行器白名单和 Agent 设置，之后两个 Bot 各自独立。` });
      setCopyFrom(''); onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setWork(''); setSaving(false); }
  }
  const installing = conn?.clis.some(c => c.install?.state === 'running') ?? false;
  // 安装在后台进行：装的时候每 1.5 秒刷新一次安装进度（probe=0，不重复起 --version），装完自动停。
  useEffect(() => {
    if (!installing) return;
    const timer = setInterval(() => {
      target.connections(undefined, false).then(c => setConn(prev => prev ? { ...prev, clis: c.clis } : c)).catch(() => {});
    }, 1500);
    return () => clearInterval(timer);
  }, [installing, target]);
  async function install(id: string) {
    setNotice(undefined);
    try {
      const c = await target.installCLI(id);
      setConn(prev => prev ? { ...prev, clis: c.clis } : c);
    } catch (e) { setNotice({ error: true, text: message(e) }); }
  }
  async function detect() {
    setDetecting(true);
    try {
      const c = await target.connections();
      setConn(prev => prev ? { ...prev, clis: c.clis } : c);
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setDetecting(false); }
  }

  const scope = (
    <div className="bot-scope">
      <div className="bot-scope-label"><span>正在设置</span>{dirty && <em>先保存或放弃修改，再切换 Bot</em>}</div>
      <div className="bot-scope-chips" role="radiogroup" aria-label="选择要设置的 Bot">
        {bots.map(b => <button key={b.id} type="button" role="radio" aria-checked={b.id === bot.id} className="bot-scope-chip"
          disabled={busy || (dirty && b.id !== bot.id)} onClick={() => onSelectBot(b.id)}>
          <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={18} /><span>{b.name}</span>
        </button>)}
      </div>
    </div>
  );

  const feedback = notice && <p role={notice.error ? 'alert' : 'status'} className={`settings-feedback${notice.error ? ' is-error' : ''}`}>{notice.error ? <CircleAlert size={15} /> : <Check size={15} />}<span>{notice.text}</span></p>;

  if (!state || !conn || !exec) {
    return <div className="settings-channel-page">
      <h3 id="settings-models-title">模型与 Agent</h3>
      {scope}{feedback}
      <div className="settings-load">{notice ? <button className="settings-button" onClick={() => { setNotice(undefined); setAttempt(n => n + 1); }}>重试</button> : <p className="settings-pending"><Loader2 size={16} className="animate-spin" />正在读取「{bot.name}」的设置…</p>}</div>
    </div>;
  }

  const footer = <footer className="settings-channels-footer">
    <span className={dirty && invalidReason ? 'is-warn' : undefined}>{dirty && invalidReason ? <CircleAlert size={13} /> : <Save size={13} />}{dirty ? invalidReason || '有未保存的修改' : `已保存 · 只影响「${bot.name}」`}</span>
    <div className="settings-footer-actions">
      {dirty && <button type="button" className="settings-button" disabled={busy} onClick={discard}><Undo2 size={14} />放弃修改</button>}
      <button type="button" className="settings-button settings-primary" disabled={busy || !dirty || !valid} onClick={save}>{saving && !work && <Loader2 size={14} className="animate-spin" />}保存设置</button>
    </div>
  </footer>;

  const routePicker = (id: string, value: string, setID: (v: string) => void, m: string, setM: (v: string) => void, label: string, touch: () => void = changed) => (
    <div className="settings-channel-form-grid">
      <div className="settings-field"><label htmlFor={`${label}-channel`}>API 渠道</label>
        <select id={`${label}-channel`} value={value} disabled={busy} onChange={e => { const p = channels.find(c => c.id === e.target.value); setID(e.target.value); setM(p ? visible(p)[0] ?? '' : ''); touch(); }}>
          <option value="">{label === 'chat' ? '不设置' : '选择渠道'}</option>
          {channels.map(p => <option key={p.id} value={p.id} disabled={!p.enabled && p.id !== value}>{p.name}{p.enabled ? '' : '（已停用）'}</option>)}
        </select></div>
      <div className="settings-field"><label htmlFor={`${label}-model`}>模型</label>
        <select id={`${label}-model`} value={m} disabled={busy} onChange={e => { setM(e.target.value); touch(); }}>
          <option value="">选择模型</option>
          {channels.find(p => p.id === id) && visible(channels.find(p => p.id === id)!).map(x => <option key={x}>{x}</option>)}
        </select></div>
    </div>
  );

  const noChannels = channels.length === 0;
  const fallback = state.env_override ? null : noChannels && <p className="model-card-warn"><CircleAlert size={14} />还没有 API 渠道。<button type="button" className="settings-link" onClick={onOpenChannels}>去添加 API 渠道</button></p>;
  const stale = (id: string) => { const c = channels.find(p => p.id === id); return id && (!c || !c.enabled); };
  const staleWarn = (id: string) => stale(id) && <p className="model-card-warn is-error"><CircleAlert size={14} />之前选的渠道已被停用或删除，请重新选择。</p>;
  const manage = !noChannels && <button type="button" className="settings-link model-manage" onClick={onOpenChannels}><Plug size={12} />管理 API 渠道</button>;

  return <div className="settings-channel-page">
    <div className="settings-channel-title"><div><h3 id="settings-models-title">模型与 Agent</h3>
      <p className="settings-description">API 渠道由所有 Bot 共用，这里为每个 Bot 单独选择聊天模型、执行器白名单和默认执行器，互不影响。</p></div></div>
    {scope}
    {state.env_override && <p className="settings-env-note">启动 reeve 时设置了环境变量 BASE_URL / CHAT_MODEL，所有 Bot 的 API 模型暂时固定为：聊天 <code>{state.env_model}</code>、Agent <code>{state.env_agent_model}</code>。这里保存的内容要去掉环境变量并重启后才生效；外部执行器不受影响。</p>}
    {feedback}
    {others.length > 0 && <div className="model-copy">
      <Copy size={14} aria-hidden="true" /><span>沿用其他 Bot 的选择</span>
      <select aria-label="复制来源" value={copyFrom} disabled={busy} onChange={e => setCopyFrom(e.target.value)}>
        <option value="">选择 Bot…</option>
        {others.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
      </select>
      <button type="button" className="settings-button" disabled={busy || !copyFrom || dirty} title={dirty ? '先保存或放弃当前修改' : undefined} onClick={copySettings}>
        {work === 'copy' ? <Loader2 size={14} className="animate-spin" /> : <Copy size={14} />}复制
      </button>
      <small>复制聊天模型、Agent 模型、执行器白名单和默认执行器，会替换「{bot.name}」当前的选择。</small>
    </div>}

    <section className="model-card" aria-labelledby="chat-model-title">
      <header className="model-card-head"><span className="model-card-icon"><MessageSquare size={17} /></span>
        <div><h4 id="chat-model-title">聊天模型</h4><p>回复你的消息、决定什么时候派出后台任务。</p></div>
        <span className="settings-agent-status">{chatSet && chatValid && route ? `${route.name} · ${model}` : !chatSet ? (mode === 'cli' ? '未设置 · 消息直接交给 CLI' : '未设置') : '请选择模型'}</span>
      </header>
      {noChannels ? fallback : <>{staleWarn(defaultID)}{routePicker(defaultID, defaultID, setDefaultID, model, setModel, 'chat')}{manage}</>}
      {!noChannels && !chatSet && mode === 'api' && <p className="settings-field-hint">没有聊天模型时，这个 Bot 不能回复消息；只用外部执行器时可以不设置。</p>}
    </section>

    <section className="model-card" aria-labelledby="executors-title">
      <header className="model-card-head"><span className="model-card-icon"><Terminal size={17} /></span>
        <div><h4 id="executors-title">执行器</h4><p>任务可以交给哪些执行器，以及新任务默认用哪一个。</p></div>
        <span className="settings-agent-status" title={executorNames[execDefault]}>{execDefault === 'builtin' ? '默认：内置执行器' : `默认：${executorNames[execDefault] ?? execDefault}`}</span>
      </header>
      <div className="settings-field">
        <div className="settings-field-head"><label>执行器白名单</label>
          <button type="button" className="settings-button" disabled={busy || detecting} onClick={detect}><RefreshCw size={14} className={detecting ? 'animate-spin' : ''} />{detecting ? '正在检测…' : '重新检测'}</button></div>
        <ul className="grid gap-2" role="group" aria-label="执行器白名单">
          <li className="rounded-xl border border-line bg-s2 px-3 py-2.5">
            <label className="flex items-start gap-2.5">
              <input type="checkbox" checked disabled className="mt-0.5" aria-label="内置执行器（始终可用）" />
              <span className="grid min-w-0 gap-1"><b className="text-[12.5px] font-medium text-fg">内置执行器</b>
                <small className="text-[11px] leading-relaxed text-fg3">内置执行器始终可用，不能取消。</small></span>
            </label>
          </li>
          {conn.clis.map(c => {
            const on = allowed.includes(c.id);
            const unavailable = executorUnavailable(c);
            const state = availabilityLabel(c);
            // 不可用的 CLI 不能再勾选；已经勾上的仍允许取消，免得配置卡死。
            return <li key={c.id} className={`rounded-xl border px-3 py-2.5 ${on ? 'border-fg3 bg-s1' : 'border-line bg-s2'}`}>
              <label className="flex items-start gap-2.5">
                <input type="checkbox" checked={on} disabled={busy || (unavailable && !on)} className="mt-0.5"
                  onChange={e => toggleExecutor(c.id, e.target.checked)} />
                <span className="grid min-w-0 gap-1">
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <b className="text-[12.5px] font-medium text-fg">{c.name}</b>
                    <small className="text-[11px] text-fg3">{c.installed ? '已安装' : '未安装'}</small>
                    <small className="text-[11px] text-fg3">版本 {c.version ? `v${c.version}` : '未知'}</small>
                    <small className={state === '可用' ? 'text-[11px] text-ok' : state === '不可用' ? 'text-[11px] text-warn' : 'text-[11px] text-fg4'}>{state}</small>
                  </span>
                  <small className="text-[11px] leading-relaxed text-fg3">登录要求：{c.subscription ?? '以 CLI 自身配置为准'}{c.login ? <>（<code>{c.login}</code>）</> : null}</small>
                  {unavailable && c.note && <small className="text-[11px] leading-relaxed text-warn">{c.note}</small>}
                </span>
              </label>
            </li>;
          })}
        </ul>
        {externalChecked && <p role="note" className="flex items-start gap-2 rounded-lg border border-warn/40 bg-warn-soft px-3 py-2 text-[11.5px] leading-relaxed text-warn">
          <CircleAlert size={14} className="mt-px shrink-0" />外部 CLI 等于把整台机器的执行权交给它，只勾选你信任的 CLI。
        </p>}
      </div>
      <div className="settings-field">
        <label>默认执行器</label>
        <div className="grid gap-1.5" role="radiogroup" aria-label="默认执行器">
          {allowed.map(id => {
            const meta = conn.clis.find(c => c.id === id);
            return <label key={id} className={`flex items-center gap-2.5 rounded-lg border px-3 py-2 ${execDefault === id ? 'border-fg3 bg-s1' : 'border-line bg-s2'}`}>
              <input type="radio" name="executor-default" checked={execDefault === id} disabled={busy} onChange={() => pickDefault(id)} />
              <span className="text-[12.5px] text-fg">{executorNames[id] ?? id}</span>
              {executorUnavailable(meta) && <small className="text-[11px] text-warn">不可用</small>}
              {id !== 'builtin' && <small className="ml-auto font-mono text-[10.5px] text-fg4">外部 CLI</small>}
            </label>;
          })}
        </div>
        <p className="settings-field-hint">新任务默认用这个执行器；聊天里也可以让它换用白名单里的其他执行器。</p>
      </div>
      <CLIInstalls clis={conn.clis} busy={busy} installing={installing} onInstall={install} />
    </section>

    <section className="model-card" aria-labelledby="agent-model-title">
      <header className="model-card-head"><span className="model-card-icon"><Workflow size={17} /></span>
        <div><h4 id="agent-model-title">Agent</h4><p>执行后台任务：读写工作区文件、运行命令、调用工具。</p></div>
      </header>
      <div className="agent-mode" role="radiogroup" aria-label="Agent 执行方式">
        <button type="button" role="radio" aria-checked={mode === 'api'} disabled={busy} onClick={() => pickDefault('builtin')}>
          <Bot size={16} /><span><b>API 模型</b><small>reeve 内置执行器，按 API 用量计费</small></span>
        </button>
        <button type="button" role="radio" aria-checked={mode === 'cli'} disabled={busy} onClick={() => {
          // 只看真正探测过且可用的 CLI：未探测（probed=false）不能当成可用。
          const first = allowedExternal.find(id => {
            const c = conn.clis.find(cli => cli.id === id);
            return c?.probed === true && c.available === true;
          });
          if (first) pickDefault(first);
          else setNotice({ error: true, text: '先在执行器白名单里勾选一个可用的外部执行器，再切到外部 Agent。' });
        }}>
          <BadgeCheck size={16} /><span><b>外部 Agent</b><small>白名单里的 CLI，在本机执行整个任务</small></span>
        </button>
      </div>
      {mode === 'api' ? <div className="agent-mode-body">
        {noChannels ? fallback : <>
          {staleWarn(agentProviderID)}
          {routePicker(agentProviderID, agentProviderID, setAgentProviderID, agentModel, setAgentModel, 'agent')}
          {chatValid && (agentProviderID !== defaultID || agentModel !== model) && <button type="button" className="settings-link agent-same" disabled={busy} onClick={() => { setAgentProviderID(defaultID); setAgentModel(model); changed(); }}>改为与聊天模型相同</button>}
        </>}
      </div> : <div className="agent-mode-body">
        <p className="settings-field-hint">默认执行器：<b>{executorNames[execDefault] ?? execDefault}</b>。外部 CLI 会在本机执行整个任务，它内部的命令不经过 reeve 的审批。</p>
        {allowedExternal.length > 1 && <div className="settings-field"><label htmlFor="executor-options">执行器选项</label>
          <select id="executor-options" value={activeOptID} disabled={busy} onChange={e => setOptID(e.target.value)}>
            {allowedExternal.map(id => <option key={id} value={id}>{executorNames[id] ?? id}</option>)}
          </select>
        </div>}
        {activeOptID === 'pi' ? <div className="agent-cli-pi">
          <p className="settings-field-hint">Pi 通过 reeve 的本机网关调用下面选的 API 渠道和模型；渠道的 Key 留在 reeve 里，Pi 和它执行的命令都拿不到。</p>
          {noChannels ? fallback : <>
            {staleWarn(editingOpt.cli_channel ?? '')}
            {routePicker(editingOpt.cli_channel ?? '', editingOpt.cli_channel ?? '', v => setOption('pi', { cli_channel: v }), editingOpt.cli_model ?? '', v => setOption('pi', { cli_model: v }), 'pi', () => setNotice(undefined))}
            {manage}
          </>}
        </div> : editingCLI && <CLIDetails c={editingCLI} model={editingOpt.cli_model ?? ''} setModel={v => setOption(activeOptID, { cli_model: v })}
          auth={editingAuth} setAuth={v => setOption(activeOptID, { cli_auth: v })} busy={busy} />}
        {!chatSet && <p className="settings-field-hint">这个 Bot 没有聊天模型时，你发的消息会直接交给默认执行器处理。</p>}
      </div>}
    </section>
    {footer}
  </div>;
}

function CLIDetails({ c, model, setModel, auth, setAuth, busy }: {
  c: AgentCLI; model: string; setModel: (v: string) => void; auth: CLIAuth; setAuth: (v: CLIAuth) => void; busy: boolean;
}) {
  const listID = `cli-models-${c.id}`;
  return <div className="agent-cli-details">
    <div className="settings-field">
      <label htmlFor="cli-model">模型</label>
      {c.model_option ? <>
        <input id="cli-model" list={c.models?.length ? listID : undefined} value={model} disabled={busy} spellCheck={false} autoComplete="off"
          placeholder={`留空使用 ${c.name} 的默认模型`} onChange={e => setModel(e.target.value)} />
        {c.models?.length ? <datalist id={listID}>{c.models.map(m => <option key={m} value={m} />)}</datalist> : null}
        <p className="settings-field-hint">{c.models?.length ? `可填 ${c.models.join(' / ')} 或完整模型 ID。` : '填写该 CLI 支持的模型 ID。'}订阅可用的模型以你的套餐为准。</p>
      </> : <p className="settings-field-hint agent-cli-static">使用 {c.name} 自己的模型设置（在 CLI 内调整）。</p>}
    </div>
    <fieldset className="agent-auth" disabled={busy}>
      <legend>认证</legend>
      <label className={auth === 'subscription' ? 'is-on' : ''}><input type="radio" name="cli-auth" checked={auth === 'subscription'} onChange={() => setAuth('subscription')} />
        <span><b>官方订阅（推荐）</b><small>只用 CLI 登录的 {c.subscription ?? '官方'} 账户；即使进程里设置了 API Key 环境变量也不会改走按量计费。</small></span></label>
      <label className={auth === 'env' ? 'is-on' : ''}><input type="radio" name="cli-auth" checked={auth === 'env'} onChange={() => setAuth('env')} />
        <span><b>沿用进程环境变量</b><small>保留 reeve 进程里的 API Key / Base URL 环境变量，由 CLI 自行决定用哪种认证。</small></span></label>
    </fieldset>
    {c.login && <p className="agent-login"><Terminal size={13} />还没登录？在服务器终端运行 <code>{c.login}</code> 完成登录。</p>}
  </div>;
}

/** 一键安装 / 更新：reeve 把 Node.js 和 CLI 装到数据目录，不需要 root，也不改系统目录。 */
function CLIInstalls({ clis, busy, installing, onInstall }: {
  clis: AgentCLI[]; busy: boolean; installing: boolean; onInstall: (id: string) => void;
}) {
  const rows = clis.filter(c => c.installable || c.install);
  if (!rows.length) return null;
  return <div className="cli-installs">
    <div className="cli-installs-head"><h4>安装与更新</h4>
      <small>装到 reeve 数据目录（自带 Node.js），不需要 root。</small></div>
    <ul>
      {rows.map(c => {
        const job = c.install;
        const running = job?.state === 'running';
        const status = running ? `正在安装：${job?.step || '准备'}…`
          : job?.state === 'failed' ? `安装失败：${job.error}`
          : job?.state === 'done' ? `已安装 ${job.version ?? ''}`.trim()
          : c.installed ? `${c.managed ? '已由 reeve 安装' : `已安装在 ${c.path ?? '系统 PATH'}`}${c.version ? `（v${c.version}）` : ''}` : '未安装';
        return <li key={c.id} className={job?.state === 'failed' ? 'is-failed' : ''}>
          <div className="cli-install-text"><b>{c.name}</b><small title={job?.error ?? c.note ?? c.path}>{status}</small></div>
          {c.installable && <button type="button" className="settings-button" disabled={busy || installing} onClick={() => onInstall(c.id)}>
            {running ? <Loader2 size={14} className="animate-spin" /> : c.managed ? <RefreshCw size={14} /> : <Download size={14} />}
            {running ? '安装中' : c.managed ? '更新' : '安装'}</button>}
          {job?.state === 'failed' && job.log && <details className="cli-install-log"><summary>查看日志</summary><pre>{job.log}</pre></details>}
        </li>;
      })}
    </ul>
  </div>;
}
