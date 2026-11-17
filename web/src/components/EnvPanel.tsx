import { Check, CircleAlert, Loader2, Plus, RotateCcw, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type EnvVars } from '../api';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';

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
 * 实例级环境变量：所有 Bot 共用同一份，注入每个任务/命令。
 * 保存是整体替换（PUT）：先把表格合并成一个对象，再写回，所以删除行也靠保存生效。
 */
export default function EnvPanel({ saving, setSaving, onDirty }: {
  saving: boolean;
  setSaving: (v: boolean) => void;
  onDirty: (dirty: boolean) => void;
}) {
  const [rows, setRows] = useState<Row[]>([]);
  const [draft, setDraft] = useState<Draft>({ name: '', value: '' });
  const [dirty, setDirty] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    let alive = true;
    setLoaded(false);
    setLoadError('');
    api.env.get(ctl.signal)
      .then((vars) => {
        if (!alive) return;
        setRows(rowsOf(vars));
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

  // 新增行的草稿也算未保存内容：直接切 tab / 关弹窗会丢，必须让外层先确认。
  const draftPending = draft.name.trim() !== '' || draft.value !== '';
  useEffect(() => { onDirty(dirty || draftPending); }, [dirty, draftPending, onDirty]);

  const edit = (key: string, patch: Partial<Row>) => {
    setRows((list) => list.map((r) => (r.key === key ? { ...r, ...patch } : r)));
    setDirty(true);
    setNotice(undefined);
  };
  const remove = (key: string) => {
    setRows((list) => list.filter((r) => r.key !== key));
    setDirty(true);
    setNotice(undefined);
  };
  const add = () => {
    const name = draft.name.trim();
    if (!name) return;
    setRows((list) => [...list.filter((r) => r.name !== name), { key: Math.random().toString(36).slice(2), name, value: draft.value }]);
    setDraft({ name: '', value: '' });
    setDirty(true);
    setNotice(undefined);
  };

  async function save() {
    const vars = varsOf(rows);
    setSaving(true);
    setNotice(undefined);
    try {
      const res = await api.env.save(vars);
      setRows(rowsOf(vars));
      setDirty(false);
      setNotice(res.warnings.length
        ? { tone: 'warn', text: `已保存 ${Object.keys(vars).length} 个变量，但有 ${res.warnings.length} 条提示：${res.warnings.join('；')}` }
        : { tone: 'ok', text: `已保存 ${Object.keys(vars).length} 个变量` });
    } catch (e) {
      setNotice({ tone: 'error', text: message(e) });
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <h3 id="settings-env-title">环境变量</h3>
      <p className="settings-description">
        实例级变量，所有 Bot 共用，会注入每个任务和命令。注册成常驻服务时，会从当前 shell 导入
        PATH、时区/语言、代理与 TLS 证书设置；之后可用 <code>reeve service env refresh</code> 更新。
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
        <section className="settings-env-scope" aria-label="实例级环境变量">
          <div className="settings-env-head">
            <div>
              <h4>实例级</h4>
              <p>所有 Bot 共用，保存在实例目录的 env 文件里。</p>
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
                    <td><input aria-label="环境变量名" spellCheck={false} autoComplete="off" value={row.name} disabled={saving}
                      onChange={(e) => edit(row.key, { name: e.target.value })} /></td>
                    <td><input aria-label={`环境变量 ${row.name} 的值`} spellCheck={false} autoComplete="off" value={row.value} disabled={saving}
                      onChange={(e) => edit(row.key, { value: e.target.value })} /></td>
                    <td className="settings-env-actions">
                      <button type="button" className="settings-link" aria-label={`删除 ${row.name}`} disabled={saving} onClick={() => remove(row.key)}>
                        <Trash2 size={14} aria-hidden="true" />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <form className="settings-env-add" onSubmit={(e) => { e.preventDefault(); add(); }}>
            <input aria-label="新增环境变量名" placeholder="NAME" spellCheck={false} autoComplete="off" value={draft.name} disabled={saving}
              onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))} />
            <input aria-label="新增环境变量的值" placeholder="值" spellCheck={false} autoComplete="off" value={draft.value} disabled={saving}
              onChange={(e) => setDraft((d) => ({ ...d, value: e.target.value }))} />
            <button type="submit" className="settings-button" disabled={saving || !draft.name.trim()}><Plus size={14} aria-hidden="true" />添加</button>
          </form>
          {notice && (
            <p role={notice.tone === 'error' ? 'alert' : 'status'} className={`settings-feedback${notice.tone === 'error' ? ' is-error' : ''}${notice.tone === 'warn' ? ' is-warn' : ''}`}>
              {notice.tone === 'ok' ? <Check size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}
              <span>{notice.text}</span>
            </p>
          )}
          <div className="settings-actions">
            <span className="settings-save-note">保存会用当前表格整体替换实例级变量</span>
            <button type="button" className="settings-button settings-primary" disabled={saving || !dirty} onClick={() => save()}>
              {saving && dirty && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
              {dirty ? '保存修改' : '已保存'}
            </button>
          </div>
        </section>
      )}
    </>
  );
}
