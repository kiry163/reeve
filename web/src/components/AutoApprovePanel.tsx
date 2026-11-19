import { Check, CircleAlert, Loader2, RotateCcw } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { api } from '../api';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';

type Notice = { tone: 'ok' | 'warn' | 'error'; text: string };

const maxCommands = 200;
const maxCommandLen = 512;
const unsafeShell = /[;&|<>`$\n\r]/;

function lineError(line: string): string {
  if (new TextEncoder().encode(line).length > maxCommandLen) return `命令太长（最多 ${maxCommandLen} 字节）`;
  if (/[\u0000-\u001f\u007f]/.test(line)) return '不能包含控制字符';
  if (unsafeShell.test(line)) return '不能包含 ; & | < > $ 或反引号';
  if (line === '*') return '不能只用 *，会放行所有命令';
  return '';
}

function parseCommands(text: string): { commands: string[]; error?: string } {
  const lines = text.split('\n');
  const seen = new Set<string>();
  const commands: string[] = [];
  let count = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    count++;
    const err = lineError(line);
    if (err) return { commands: [], error: `第 ${i + 1} 行：${err}` };
    if (seen.has(line)) continue;
    seen.add(line);
    commands.push(line);
  }
  if (count > maxCommands) return { commands: [], error: `最多保存 ${maxCommands} 条命令` };
  return { commands };
}

const matches = (pattern: string, command: string) =>
  pattern.endsWith('*') ? command.startsWith(pattern.slice(0, -1)) : command === pattern;

/**
 * 实例级自动审批命令白名单：所有 Bot 共用。
 * 命中后 shell 命令跳过审批卡；危险命令和含 shell 控制符的复合命令仍走审批。
 */
export default function AutoApprovePanel({ saving, setSaving, onDirty }: {
  saving: boolean;
  setSaving: (v: boolean) => void;
  onDirty: (dirty: boolean) => void;
}) {
  const [text, setText] = useState('');
  const [dirty, setDirty] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [probe, setProbe] = useState('');

  useEffect(() => {
    const ctl = new AbortController();
    let alive = true;
    setLoaded(false);
    setLoadError('');
    api.approvalWhitelist.get(ctl.signal)
      .then((v) => {
        if (!alive) return;
        setText(v.commands.join('\n'));
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

  const parsed = useMemo(() => parseCommands(text), [text]);

  const preview = useMemo(() => {
    const command = probe.trim();
    if (!command) return undefined;
    if (parsed.error) return { tone: 'warn' as const, text: `先修正名单：${parsed.error}` };
    if (unsafeShell.test(command)) {
      return { tone: 'warn' as const, text: '含 shell 控制符，不会自动放行，仍会按审批策略处理。' };
    }
    const hit = parsed.commands.find((pattern) => matches(pattern, command));
    return hit
      ? { tone: 'ok' as const, text: `命中「${hit}」，会自动放行；危险命令规则仍可能拦截。` }
      : { tone: 'muted' as const, text: '没有命中白名单，仍会按审批策略处理。' };
  }, [probe, parsed]);

  const touch = (value: string) => {
    setText(value);
    setDirty(true);
    setNotice(undefined);
  };

  async function save() {
    const result = parseCommands(text);
    if (result.error) {
      setNotice({ tone: 'error', text: result.error });
      return;
    }
    setSaving(true);
    setNotice(undefined);
    try {
      const v = await api.approvalWhitelist.save(result.commands);
      setText(v.commands.join('\n'));
      setDirty(false);
      setNotice({ tone: 'ok', text: `已保存 ${v.commands.length} 条命令，立即对所有 Bot 生效` });
    } catch (e) {
      setNotice({ tone: 'error', text: message(e) });
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <h3 id="settings-approve-title">自动审批</h3>
      <p className="settings-description">
        实例级 shell 命令白名单，所有 Bot 共用。命中名单的命令不再弹审批卡，直接执行。
        危险命令（rm -rf、sudo、curl | sh、git push 等）仍然优先询问或拒绝。
        只作用于内置 shell 工具；codex / pi / omp 等外部执行器内部的命令不受这里控制。
      </p>
      {!loaded && !loadError && <p className="settings-pending"><Loader2 size={15} className="animate-spin" aria-hidden="true" /><span>正在读取自动审批设置…</span></p>}
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
          <p className="settings-env-note">
            白名单等于预先授权：只加入你信任的命令。命中后模型可以在无人确认的情况下执行它，
            请优先使用精确匹配，谨慎使用前缀。
          </p>

          <div className="settings-field">
            <label htmlFor="approve-commands">命令名单</label>
            <textarea id="approve-commands" rows={12} value={text} disabled={saving}
              spellCheck={false} autoComplete="off"
              placeholder={'go test*\ngo build*\nnpm run lint*\ngit status'}
              onChange={(e) => touch(e.target.value)} />
            <span className="settings-field-hint">
              每行一条，区分大小写。行尾的 * 表示前缀匹配（<code>go test*</code> 会匹配
              <code>go test ./...</code>），其他位置的 * 按字面量匹配。
              含 <code>;</code> <code>&amp;&amp;</code> <code>|</code>
              <code>&gt;</code> <code>&lt;</code> <code>$</code> 或反引号的复合命令，以及带调用级
              env 的 shell 调用，都不会自动放行。
            </span>
          </div>

          <div className="settings-field">
            <label htmlFor="approve-probe">检查一条命令</label>
            <input id="approve-probe" value={probe} disabled={saving} spellCheck={false} autoComplete="off"
              placeholder="go test ./..." onChange={(e) => setProbe(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') e.preventDefault(); }} />
            {preview && (
              <span className={`settings-field-hint approve-preview is-${preview.tone}`}>{preview.text}</span>
            )}
          </div>

          {notice && (
            <p role={notice.tone === 'error' ? 'alert' : 'status'}
              className={`settings-feedback${notice.tone === 'error' ? ' is-error' : ''}${notice.tone === 'warn' ? ' is-warn' : ''}`}>
              {notice.tone === 'ok' ? <Check size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}
              <span>{notice.text}</span>
            </p>
          )}

          <div className="settings-actions">
            <span className="settings-save-note">保存后立即对所有 Bot 生效</span>
            <button type="submit" className="settings-button settings-primary" disabled={saving || !dirty}>
              {saving && dirty && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
              {dirty ? '保存修改' : '已保存'}
            </button>
          </div>
        </form>
      )}
    </>
  );
}
