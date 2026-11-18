import { Check, CircleAlert, Copy, Eye, EyeOff, Loader2, RotateCcw, Send } from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type PushDraft } from '../api';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';

type Notice = { tone: 'ok' | 'warn' | 'error'; text: string };

/**
 * 实例级 ntfy 推送：服务端把审批/任务终态/助手回复发到自建 ntfy，手机端由 ntfy App 接收。
 * token 只写不读；留空保留，清除则发空串。
 */
export default function PushPanel({ saving, setSaving, onDirty }: {
  saving: boolean;
  setSaving: (v: boolean) => void;
  onDirty: (dirty: boolean) => void;
}) {
  const [enabled, setEnabled] = useState(false);
  const [url, setURL] = useState('');
  const [topic, setTopic] = useState('');
  const [publicHost, setPublicHost] = useState('');
  const [approval, setApproval] = useState(true);
  const [task, setTask] = useState(true);
  const [reply, setReply] = useState(true);
  const [token, setToken] = useState('');
  const [hasToken, setHasToken] = useState(false);
  const [clearToken, setClearToken] = useState(false);
  const [showToken, setShowToken] = useState(false);

  const [dirty, setDirty] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    let alive = true;
    setLoaded(false);
    setLoadError('');
    api.push.get(ctl.signal)
      .then((v) => {
        if (!alive) return;
        setEnabled(v.enabled);
        setURL(v.url);
        setTopic(v.topic);
        setPublicHost(v.public_host);
        setApproval(v.approval);
        setTask(v.task);
        setReply(v.reply);
        setHasToken(v.has_token);
        setToken('');
        setClearToken(false);
        setDirty(false);
        setNotice(undefined);
        setLoaded(true);
      })
      .catch((e) => {
        if (!alive) return;
        setLoadError(message(e));
        setLoaded(true);
      });
    return () => { alive = false; ctl.abort(); };
  }, [attempt]);

  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);

  const touch = () => { setDirty(true); setNotice(undefined); };

  function draft(): PushDraft {
    const d: PushDraft = {
      enabled, url: url.trim(), topic: topic.trim(), public_host: publicHost.trim(),
      approval, task, reply,
    };
    if (clearToken) d.token = '';
    else if (token.trim()) d.token = token.trim();
    return d;
  }

  async function save(): Promise<boolean> {
    if (enabled && (!url.trim() || !topic.trim())) {
      setNotice({ tone: 'error', text: '启用推送前请填写 ntfy 地址和主题' });
      return false;
    }
    setSaving(true);
    setNotice(undefined);
    try {
      const v = await api.push.save(draft());
      setEnabled(v.enabled);
      setURL(v.url);
      setTopic(v.topic);
      setPublicHost(v.public_host);
      setApproval(v.approval);
      setTask(v.task);
      setReply(v.reply);
      setHasToken(v.has_token);
      setToken('');
      setClearToken(false);
      setDirty(false);
      setNotice({ tone: 'ok', text: '推送设置已保存' });
      return true;
    } catch (e) {
      setNotice({ tone: 'error', text: message(e) });
      return false;
    } finally {
      setSaving(false);
    }
  }

  async function test() {
    if (dirty && !(await save())) return;
    setTesting(true);
    setNotice(undefined);
    try {
      const r = await api.push.test();
      setNotice(r.ok
        ? { tone: 'ok', text: '测试通知已发送，检查手机上的 ntfy' }
        : { tone: 'error', text: '测试通知发送失败' });
    } catch (e) {
      setNotice({ tone: 'error', text: message(e) });
    } finally {
      setTesting(false);
    }
  }

  function copy(text: string) {
    const p = navigator.clipboard?.writeText(text);
    if (!p) {
      setNotice({ tone: 'warn', text: '复制失败，请手动选择文本' });
      return;
    }
    p.then(() => setNotice({ tone: 'ok', text: '订阅地址已复制' }))
      .catch(() => setNotice({ tone: 'warn', text: '复制失败，请手动选择文本' }));
  }

  const subscribeURL = (() => {
    try {
      if (!url.trim() || !topic.trim()) return '';
      const u = new URL(url.trim());
      if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
      return `ntfy://${u.host}/${topic.trim()}`;
    } catch {
      return '';
    }
  })();
  const insecure = /^http:\/\//i.test(url.trim())
    && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(?::|\/|$)/i.test(url.trim());

  return (
    <>
      <h3 id="settings-push-title">推送</h3>
      <p className="settings-description">
        通过自建 ntfy 把审批请求、任务结束和新回复推到手机，不依赖 FCM/Google。手机端需要安装 ntfy
        （F-Droid 或 GitHub 上的无 FCM 构建），订阅下面的地址，并在 Android App 的「设置 › 通知」里打开
        「使用 ntfy 外部推送」。
      </p>
      {!loaded && !loadError && <p className="settings-pending"><Loader2 size={15} className="animate-spin" aria-hidden="true" /><span>正在读取推送设置…</span></p>}
      {loadError && (
        <>
          <p role="alert" className="settings-feedback is-error"><CircleAlert size={15} aria-hidden="true" /><span>{loadError}</span></p>
          <div className="settings-actions">
            <button type="button" className="settings-button" onClick={() => setAttempt((n) => n + 1)}><RotateCcw size={14} aria-hidden="true" />重试</button>
          </div>
        </>
      )}
      {loaded && !loadError && (
        <form className="settings-fields" onSubmit={(e) => { e.preventDefault(); void save(); }}>
          <div className="settings-field">
            <label>
              <input type="checkbox" checked={enabled} disabled={saving}
                onChange={(e) => { setEnabled(e.target.checked); touch(); }} />
              启用 ntfy 推送
            </label>
            <span className="settings-field-hint">关闭后服务端不再发通知；「发送测试通知」仍然可用。</span>
          </div>

          <div className="settings-field">
            <label htmlFor="push-url">ntfy 地址</label>
            <input id="push-url" value={url} placeholder="https://ntfy.example.com" spellCheck={false} autoComplete="off"
              disabled={saving} onChange={(e) => { setURL(e.target.value); touch(); }} />
            <span className="settings-field-hint">只填到域名（可带端口），不要带 topic 路径。本机和手机都要能访问。</span>
          </div>

          <div className="settings-field">
            <label htmlFor="push-topic">主题（topic）</label>
            <input id="push-topic" value={topic} placeholder="reeve-…" spellCheck={false} autoComplete="off"
              disabled={saving} onChange={(e) => { setTopic(e.target.value); touch(); }} />
            <span className="settings-field-hint">1–64 位字母、数字、下划线或连字符；建议用随机字符串当密码。</span>
          </div>

          <div className="settings-field">
            <div className="settings-field-head">
              <label htmlFor="push-token">访问 token（可选）</label>
              {hasToken && !clearToken && (
                <button type="button" className="settings-link" disabled={saving}
                  onClick={() => { setClearToken(true); setToken(''); touch(); }}>清除已保存的 token</button>
              )}
              {clearToken && (
                <button type="button" className="settings-link" disabled={saving}
                  onClick={() => { setClearToken(false); touch(); }}>撤销清除</button>
              )}
            </div>
            <div className="settings-key-input">
              <input id="push-token" type={showToken ? 'text' : 'password'} autoComplete="new-password"
                value={token} disabled={saving}
                placeholder={hasToken && !clearToken ? '已加密保存，留空保留' : '没有 token 可留空'}
                onChange={(e) => { setToken(e.target.value); setClearToken(false); touch(); }} />
              <button type="button" aria-label={showToken ? '隐藏 token' : '显示 token'}
                onClick={() => setShowToken((v) => !v)}>{showToken ? <EyeOff size={16} aria-hidden="true" /> : <Eye size={16} aria-hidden="true" />}</button>
            </div>
            <span className="settings-field-hint">
              {clearToken ? '保存时将清除已保存的 token。' : 'ntfy 开启访问控制时填写；只写不读，不会回显。'}
            </span>
          </div>

          <div className="settings-field">
            <label htmlFor="push-host">客户端访问地址（可选）</label>
            <input id="push-host" value={publicHost} placeholder="home.example.com:18800" spellCheck={false} autoComplete="off"
              disabled={saving} onChange={(e) => { setPublicHost(e.target.value); touch(); }} />
            <span className="settings-field-hint">
              填手机里 reeve 服务器的 host[:port]。填了才能从通知直达对应聊天；不填只会打开 App。
            </span>
          </div>

          <fieldset className="settings-field" disabled={saving}>
            <legend>提醒我</legend>
            <label>
              <input type="checkbox" checked={approval} onChange={(e) => { setApproval(e.target.checked); touch(); }} />
              审批请求
            </label>
            <label>
              <input type="checkbox" checked={task} onChange={(e) => { setTask(e.target.checked); touch(); }} />
              任务结束
            </label>
            <label>
              <input type="checkbox" checked={reply} onChange={(e) => { setReply(e.target.checked); touch(); }} />
              助手新回复
            </label>
          </fieldset>

          {insecure && (
            <p className="settings-feedback is-warn">
              <CircleAlert size={15} aria-hidden="true" />
              <span>ntfy 地址不是 HTTPS：topic 和 token 会明文经过网络，建议改用 HTTPS。</span>
            </p>
          )}

          {subscribeURL && (
            <div className="settings-field">
              <label htmlFor="push-subscribe">手机订阅地址</label>
              <div className="settings-key-input">
                <input id="push-subscribe" readOnly value={subscribeURL}
                  onFocus={(e) => e.currentTarget.select()} />
                <button type="button" aria-label="复制订阅地址" onClick={() => copy(subscribeURL)}>
                  <Copy size={16} aria-hidden="true" />
                </button>
              </div>
              <span className="settings-field-hint">在手机 ntfy App 里添加订阅；如果 ntfy 开了访问控制，token 也要在 App 里填。</span>
            </div>
          )}

          {notice && (
            <p role={notice.tone === 'error' ? 'alert' : 'status'}
              className={`settings-feedback${notice.tone === 'error' ? ' is-error' : ''}${notice.tone === 'warn' ? ' is-warn' : ''}`}>
              {notice.tone === 'ok' ? <Check size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}
              <span>{notice.text}</span>
            </p>
          )}

          <div className="settings-actions">
            <span className="settings-save-note">保存后即刻生效；通知正文不含聊天内容。</span>
            <button type="submit" className="settings-button settings-primary" disabled={saving || !dirty}>
              {saving && dirty && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
              {dirty ? '保存修改' : '已保存'}
            </button>
            <button type="button" className="settings-button" disabled={saving || testing || !url.trim() || !topic.trim()}
              onClick={() => void test()}>
              {testing ? <Loader2 size={14} className="animate-spin" aria-hidden="true" /> : <Send size={14} aria-hidden="true" />}
              {testing ? '发送中…' : '发送测试通知'}
            </button>
          </div>
        </form>
      )}
    </>
  );
}
