import { BookOpen, CircleAlert, Loader2, RotateCcw } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { BotProfile, SettingsAPI, SkillIndex } from '../api';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 reeve 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';

/**
 * 技能索引：只读。技能在启动时从实例级与 Bot 私有的 skills/ 目录加载，
 * 这里只展示加载结果与失败原因，不提供编辑或重载。
 */
export default function SkillsPanel({ target, bot }: { target: SettingsAPI; bot?: BotProfile }) {
  const [index, setIndex] = useState<SkillIndex>();
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    let alive = true;
    setLoaded(false);
    setError('');
    target.skills(ctl.signal)
      .then((v) => { if (alive) { setIndex(v); setLoaded(true); } })
      .catch((e) => { if (alive) { setError(message(e)); setLoaded(true); } });
    return () => { alive = false; ctl.abort(); };
  }, [target, attempt]);

  return (
    <>
      <h3 id="settings-skills-title">技能</h3>
      <p className="settings-description">
        技能来自技能目录里的 <code>SKILL.md</code>：实例级技能所有 Bot 可用，{bot?.name || '当前 Bot'} 自己的同名技能会覆盖它。
        技能只在启动时加载，新增或修改后需要重启 reeve。
      </p>
      {!loaded && !error && <p className="settings-pending"><Loader2 size={15} className="animate-spin" aria-hidden="true" /><span>正在读取技能索引…</span></p>}
      {error && (
        <>
          <p role="alert" className="settings-feedback is-error"><CircleAlert size={15} aria-hidden="true" /><span>{error}</span></p>
          <div className="settings-actions">
            <button type="button" className="settings-button" onClick={() => setAttempt((n) => n + 1)}><RotateCcw size={14} aria-hidden="true" />重试</button>
          </div>
        </>
      )}
      {index && index.errors.length > 0 && (
        <div className="settings-skill-errors" role="alert">
          <p><CircleAlert size={15} aria-hidden="true" />有 {index.errors.length} 个技能没能加载：</p>
          <ul>{index.errors.map((err) => <li key={err}>{err}</li>)}</ul>
        </div>
      )}
      {loaded && !error && (index?.skills.length ? (
        <ul className="settings-skill-list">
          {index.skills.map((sk) => (
            // 用名字做 key：path 只是相对标签，实例级与 bot 级技能可能落在同一目录下而重复。
            <li key={sk.name}>
              <div className="settings-skill-head">
                <h4>{sk.name}</h4>
                <code>{sk.path}</code>
              </div>
              {sk.description && <p>{sk.description}</p>}
              {sk.when_to_use && <p className="settings-skill-when"><span>何时使用</span>{sk.when_to_use}</p>}
            </li>
          ))}
        </ul>
      ) : (
        <div className="settings-skills-empty">
          <BookOpen size={26} aria-hidden="true" />
          <h4>还没有技能</h4>
          <p>在技能目录下新建 <code>skills/&lt;名字&gt;/SKILL.md</code>，文件开头用 YAML front matter 写 <code>name</code>、<code>description</code>、<code>when_to_use</code>，重启 reeve 后就会出现在这里。</p>
        </div>
      ))}
    </>
  );
}
