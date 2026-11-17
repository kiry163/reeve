import { ArrowUpRight, BookOpen, Braces, Check, CircleAlert, Cpu, KeyRound, LogOut, Plug, RefreshCw, X } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { api, createSettingsAPI, type BotProfile, type SettingsAPI, type UpdateStatus } from '../api';
import BotModelSettings from './BotModelSettings';
import ChannelsPage from './ChannelsPage';
import EnvPanel from './EnvPanel';
import SkillsPanel from './SkillsPanel';

const releasesPage = 'https://github.com/kiry163/reeve/releases';
const message = (err: unknown) => err instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : err instanceof Error ? err.message : '请求失败，请重试';

type Tab = 'models' | 'api' | 'env' | 'skills' | 'update' | 'account';
type Feedback = { error: boolean; text: string };

export default function SettingsDialog({ onClose, bots, initialBot, onSettingsSaved }: {
  onClose: () => void;
  bots: BotProfile[];
  initialBot: string;
  onSettingsSaved: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const [tab, setTab] = useState<Tab>('models');
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [selectedBot, setSelectedBot] = useState(initialBot);
  const bot = bots.find(b => b.id === selectedBot) ?? bots[0];
  const target = useMemo(() => createSettingsAPI(bot?.id ?? 'main'), [bot?.id]);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    title.current?.focus();
    return () => el?.close();
  }, []);

  function go(next: Tab) {
    if (next === tab || saving) return;
    if (dirty && !window.confirm('当前页有未保存的修改，确定离开吗？')) return;
    setDirty(false); setTab(next);
  }

  function close() {
    if (saving) return;
    if (dirty && !window.confirm('有未保存的修改，确定关闭设置吗？')) return;
    onClose();
  }

  const navButton = (id: Tab, icon: React.ReactNode, label: string) => (
    <button type="button" aria-current={tab === id ? 'page' : undefined} aria-controls={`settings-${id}`} disabled={saving} onClick={() => go(id)}>
      {icon} {label}
    </button>
  );

  return (
    <dialog ref={dialog} className="settings-dialog" aria-labelledby="settings-title"
      onCancel={(e) => { e.preventDefault(); close(); }}>
      <aside className="settings-sidebar">
        <div className="settings-sidebar-heading">
          <h2 ref={title} id="settings-title" tabIndex={-1}>设置</h2>
        </div>
        <nav className="settings-nav" aria-label="设置分类">
          {navButton('models', <Cpu size={16} aria-hidden="true" />, '模型与 Agent')}
          {navButton('api', <Plug size={16} aria-hidden="true" />, 'API 渠道')}
          {navButton('env', <Braces size={16} aria-hidden="true" />, '环境变量')}
          {navButton('skills', <BookOpen size={15} aria-hidden="true" />, '技能')}
          {navButton('update', <RefreshCw size={15} aria-hidden="true" />, '版本更新')}
          {navButton('account', <KeyRound size={15} aria-hidden="true" />, '账号与登录')}
        </nav>
      </aside>
      <button type="button" className="settings-close" aria-label="关闭设置" disabled={saving} onClick={close}>
        <X size={19} />
      </button>
      <div className="settings-content">
        <section id="settings-models" className="settings-pane" hidden={tab !== 'models'} aria-labelledby="settings-models-title">
          {tab === 'models' && bot && <BotModelSettings key={bot.id} target={target}
            bot={bot} bots={bots} saving={saving} setSaving={setSaving} onDirty={setDirty}
            onSelectBot={setSelectedBot} onOpenChannels={() => go('api')} onSaved={onSettingsSaved} />}
        </section>
        <section id="settings-api" className="settings-pane" hidden={tab !== 'api'} aria-labelledby="settings-api-title">
          {tab === 'api' && <ChannelsPage key={bot?.id ?? 'main'} target={target} saving={saving} setSaving={setSaving}
            onDirty={setDirty} onSaved={onSettingsSaved} />}
        </section>
        <section id="settings-env" className="settings-pane" hidden={tab !== 'env'} aria-labelledby="settings-env-title">
          {tab === 'env' && <EnvPanel saving={saving} setSaving={setSaving} onDirty={setDirty} />}
        </section>
        <section id="settings-skills" className="settings-pane" hidden={tab !== 'skills'} aria-labelledby="settings-skills-title">
          {tab === 'skills' && <SkillsPanel key={bot?.id ?? 'main'} target={target} bot={bot} />}
        </section>
        <section id="settings-update" className="settings-pane" hidden={tab !== 'update'} aria-labelledby="settings-update-title">
          {tab === 'update' && <UpdatePanel target={target} />}
        </section>
        <section id="settings-account" className="settings-pane" hidden={tab !== 'account'} aria-labelledby="settings-account-title">
          {tab === 'account' && <AccountPanel />}
        </section>
      </div>
    </dialog>
  );
}

function UpdatePanel({ target }: { target: SettingsAPI }) {
  const [version, setVersion] = useState<string>();
  const [status, setStatus] = useState<UpdateStatus>();
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    const request = new AbortController();
    target.version(request.signal).then(v => { if (!request.signal.aborted) setVersion(v); }).catch(() => {});
    return () => request.abort();
  }, [target]);

  async function check() {
    setChecking(true);
    setError('');
    try {
      setStatus(await target.checkUpdate());
    } catch (err) {
      setStatus(undefined);
      setError(message(err));
    } finally {
      setChecking(false);
    }
  }

  return (
    <>
      <h3 id="settings-update-title">版本更新</h3>
      <p className="settings-description">查看正在运行的版本，并检查 GitHub 上的最新发布。</p>
      <dl className="settings-versions">
        <div><dt>当前版本</dt><dd>{status?.current ?? version ?? '—'}</dd></div>
        {status?.latest && <div><dt>最新发布</dt><dd>{status.latest}</dd></div>}
      </dl>
      {error && <FeedbackLine feedback={{ error: true, text: error }} />}
      {status && !checking && <p role="status" className="settings-update-note">{{
        current: '当前版本无需更新。',
        available: '有新版本可用。请在服务器上安装新版并重启。',
        no_release: 'GitHub 上暂无可用的正式发布。',
        unversioned: '可以前往发布页查看最新版本。',
      }[status.state]}</p>}
      <div className="settings-actions settings-update-actions">
        <a className="settings-link" href={status?.release_url?.startsWith(releasesPage + '/tag/') ? status.release_url : releasesPage} target="_blank" rel="noreferrer">
          查看发布记录 <ArrowUpRight size={14} aria-hidden="true" />
        </a>
        <button type="button" className="settings-button settings-primary" onClick={check} disabled={checking}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : undefined} aria-hidden="true" />
          {checking ? '检查中…' : '检查更新'}
        </button>
      </div>
      <p className="settings-update-note">这里只检查版本。安装新版和重启请在运行 reeve 的服务器上完成。</p>
    </>
  );
}

function AccountPanel() {
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback>();

  async function change(e: FormEvent) {
    e.preventDefault();
    if (next !== confirm) {
      setFeedback({ error: true, text: '两次输入的新密码不一致' });
      return;
    }
    setBusy(true);
    setFeedback(undefined);
    try {
      await api.changePassword(current, next);
      setFeedback({ error: false, text: '密码已更新；其他设备上的登录已失效。' });
      setCurrent(''); setNext(''); setConfirm('');
    } catch (err) {
      setFeedback({ error: true, text: message(err) });
    } finally {
      setBusy(false);
    }
  }

  async function logout() {
    await api.logout().catch(() => {});
    location.assign('/login');
  }

  return (
    <>
      <h3 id="settings-account-title">账号与登录</h3>
      <p className="settings-description">所有环境（包括本机）都需要密码登录。修改密码会让其他设备上的登录立即失效。</p>
      <form className="settings-fields" onSubmit={change}>
        <div className="settings-field">
          <label htmlFor="account-current">当前密码</label>
          <input id="account-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </div>
        <div className="settings-field">
          <label htmlFor="account-next">新密码</label>
          <input id="account-next" type="password" autoComplete="new-password" minLength={8} value={next} onChange={(e) => setNext(e.target.value)} required />
          <span className="settings-field-hint">至少 8 个字符。</span>
        </div>
        <div className="settings-field">
          <label htmlFor="account-confirm">确认新密码</label>
          <input id="account-confirm" type="password" autoComplete="new-password" minLength={8} value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
        </div>
        {feedback && <FeedbackLine feedback={feedback} />}
        <div className="settings-actions">
          <button type="submit" className="settings-button settings-primary" disabled={busy}>
            {busy ? '保存中…' : '修改密码'}
          </button>
        </div>
      </form>
      <div className="settings-actions">
        <button type="button" className="settings-button" onClick={logout}><LogOut size={14} aria-hidden="true" />退出登录</button>
      </div>
    </>
  );
}

function FeedbackLine({ feedback }: { feedback: Feedback }) {
  return (
    <p role={feedback.error ? 'alert' : 'status'} className={feedback.error ? 'settings-feedback is-error' : 'settings-feedback'}>
      {feedback.error ? <CircleAlert size={15} aria-hidden="true" /> : <Check size={15} aria-hidden="true" />}
      <span>{feedback.text}</span>
    </p>
  );
}
