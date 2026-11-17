import { Check, CircleAlert, Loader2, Plus, RotateCcw, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type BotProfile, type EnvAPI, type EnvVars, type SettingsAPI } from '../api';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';

type Scope = 'instance' | 'bot';
type Row = { key: string; name: string; value: string };
type Draft = { name: string; value: string };
type Notice = { tone: 'ok' | 'warn' | 'error'; text: string };

const rowsOf = (vars: EnvVars): Row[] => Object.entries(vars).map(([name, value]) => ({ key: Math.random().toString(36).slice(2), name, value }));
const varsOf = (rows: Row[]): EnvVars => {
  const out: EnvVars = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (name) out[name] = row.value;
  }
  return out;
};

/**
 * 环境变量：实例级（所有 Bot 共用）与当前 Bot 级两张表。
 * 保存是整体替换（PUT）：先把表格合并成一个对象，再写回，所以删除行也靠保存生效。
 */
export default function EnvPanel({ target, bot, saving, setSaving, onDirty }: {
  target: SettingsAPI; bot?: BotProfile; saving: boolean;
  setSaving: (v: boolean) => void; onDirty: (dirty: boolean) => void;
}) {
  const [instance, setInstance] = useState<Row[]>([]);
  const [botRows, setBotRows] = useState<Row[]>([]);
  const [drafts, setDrafts] = useState<Record<Scope, Draft>>({ instance: { name: '', value: '' }, bot: { name: '', value: '' } });
  const [dirty, setDirty] = useState<Scope[]>([]);
  const [notice, setNotice] = useState<Partial<Record<Scope, Notice>>>({});
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    let alive = true;
    setLoaded(false);
    setLoadError('');
    Promise.all([api.env.get(ctl.signal), target.env.get(ctl.signal)])
      .then(([a, b]) => {
        if (!alive) return;
        setInstance(rowsOf(a));
        setBotRows(rowsOf(b));
        setDirty([]);
        setNotice({});
        setLoaded(true);
      })
      .catch((e) => {
        if (!alive) return;
        setLoadError(message(e));
        setLoaded(true);
      });
    return () => { alive = false; ctl.abort(); };
  }, [target, attempt]);

  // 新增行的草稿也算未保存内容：直接切 tab / 关弹窗会丢，必须让外层先确认。
  const draftPending = Object.values(drafts).some((d) => d.name.trim() !== '' || d.value !== '');
  useEffect(() => { onDirty(dirty.length > 0 || draftPending); }, [dirty, draftPending, onDirty]);

  const setRows = (scope: Scope, update: (rows: Row[]) => Row[]) =>
    (scope === 'instance' ? setInstance : setBotRows)(update);
  const touch = (scope: Scope) => {
    setDirty((ds) => (ds.includes(scope) ? ds : [...ds, scope]));
    setNotice((n) => ({ ...n, [scope]: undefined }));
  };
  const edit = (scope: Scope, key: string, patch: Partial<Row>) => {
    setRows(scope, (rows) => rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
    touch(scope);
  };
  const remove = (scope: Scope, key: string) => {
    setRows(scope, (rows) => rows.filter((r) => r.key !== key));
    touch(scope);
  };
  const add = (scope: Scope) => {
    const draft = drafts[scope];
    const name = draft.name.trim();
    if (!name) return;
    setRows(scope, (rows) => [...rows.filter((r) => r.name !== name), { key: Math.random().toString(36).slice(2), name, value: draft.value }]);
    setDrafts((d) => ({ ...d, [scope]: { name: '', value: '' } }));
    touch(scope);
  };

  async function save(scope: Scope) {
    const source: EnvAPI = scope === 'instance' ? api.env : target.env;
    const vars = varsOf(scope === 'instance' ? instance : botRows);
    setSaving(true);
    setNotice((n) => ({ ...n, [scope]: undefined }));
    try {
      const res = await source.save(vars);
      setRows(scope, () => rowsOf(vars));
      setDirty((ds) => ds.filter((s) => s !== scope));
      setNotice((n) => ({
        ...n,
        [scope]: res.warnings.length
          ? { tone: 'warn', text: `已保存 ${Object.keys(vars).length} 个变量，但有 ${res.warnings.length} 条提示：${res.warnings.join('；')}` }
          : { tone: 'ok', text: `已保存 ${Object.keys(vars).length} 个变量` },
      }));
    } catch (e) {
      setNotice((n) => ({ ...n, [scope]: { tone: 'error', text: message(e) } }));
    } finally {
      setSaving(false);
    }
  }

  const table = (scope: Scope, title: string, hint: string) => {
    const rows = scope === 'instance' ? instance : botRows;
    const draft = drafts[scope];
    const n = notice[scope];
    const changed = dirty.includes(scope);
    return (
      <section className="settings-env-scope" aria-label={title}>
        <div className="settings-env-head">
          <div>
            <h4>{title}</h4>
            <p>{hint}</p>
          </div>
          <span>{rows.length} 个变量</span>
        </div>
        {rows.length === 0 ? (
          <p className="settings-env-empty">还没有变量。在下面输入名称和值，点「添加」，再保存即可；也可以在聊天里发送 <code>/env set NAME=value</code>。</p>
        ) : (
          <table className="settings-env-table">
            <thead>
              <tr><th scope="col">名称</th><th scope="col">值</th><th scope="col"><span className="sr-only">操作</span></th></tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.key}>
                  <td><input aria-label={`${title}变量名`} spellCheck={false} autoComplete="off" value={row.name} disabled={saving}
                    onChange={(e) => edit(scope, row.key, { name: e.target.value })} /></td>
                  <td><input aria-label={`${title}变量 ${row.name} 的值`} spellCheck={false} autoComplete="off" value={row.value} disabled={saving}
                    onChange={(e) => edit(scope, row.key, { value: e.target.value })} /></td>
                  <td className="settings-env-actions">
                    <button type="button" className="settings-link" aria-label={`删除 ${row.name}`} disabled={saving} onClick={() => remove(scope, row.key)}>
                      <Trash2 size={14} aria-hidden="true" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <form className="settings-env-add" onSubmit={(e) => { e.preventDefault(); add(scope); }}>
          <input aria-label={`新增${title}变量名`} placeholder="NAME" spellCheck={false} autoComplete="off" value={draft.name} disabled={saving}
            onChange={(e) => setDrafts((d) => ({ ...d, [scope]: { ...d[scope], name: e.target.value } }))} />
          <input aria-label={`新增${title}变量的值`} placeholder="值" spellCheck={false} autoComplete="off" value={draft.value} disabled={saving}
            onChange={(e) => setDrafts((d) => ({ ...d, [scope]: { ...d[scope], value: e.target.value } }))} />
          <button type="submit" className="settings-button" disabled={saving || !draft.name.trim()}><Plus size={14} aria-hidden="true" />添加</button>
        </form>
        {n && (
          <p role={n.tone === 'error' ? 'alert' : 'status'} className={`settings-feedback${n.tone === 'error' ? ' is-error' : ''}${n.tone === 'warn' ? ' is-warn' : ''}`}>
            {n.tone === 'ok' ? <Check size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}
            <span>{n.text}</span>
          </p>
        )}
        <div className="settings-actions">
          <span className="settings-save-note">保存会用当前表格整体替换本层变量</span>
          <button type="button" className="settings-button settings-primary" disabled={saving || !changed} onClick={() => save(scope)}>
            {saving && changed && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
            {changed ? '保存修改' : '已保存'}
          </button>
        </div>
      </section>
    );
  };

  return (
    <>
      <h3 id="settings-env-title">环境变量</h3>
      <p className="settings-description">
        变量会注入 Bot 运行的环境。实例级所有 Bot 共用；Bot 级只影响当前的 {bot?.name || 'Bot'}，同名时优先生效。
        保存是整体替换：表格里删掉的行也会一并删除。
      </p>
      {!loaded && !loadError && <p className="settings-pending"><Loader2 size={15} className="animate-spin" aria-hidden="true" /><span>正在读取环境变量…</span></p>}
      {loadError && (
        <>
          <p role="alert" className="settings-feedback is-error"><CircleAlert size={15} aria-hidden="true" /><span>{loadError}</span></p>
          <div className="settings-actions">
            <button type="button" className="settings-button" onClick={() => setAttempt((n) => n + 1)}><RotateCcw size={14} aria-hidden="true" />重试</button>
          </div>
        </>
      )}
      {loaded && !loadError && (
        <>
          {table('instance', '实例级', '所有 Bot 共用，保存在实例目录的 env 文件里。')}
          {table('bot', `Bot 级 · ${bot?.name || '当前 Bot'}`, '只影响这个 Bot，保存在它的 env.json 里；同名时覆盖实例级。')}
        </>
      )}
    </>
  );
}
