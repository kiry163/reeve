import { History, PanelLeft, Pencil, Plus, Settings as SettingsIcon } from 'lucide-react';
import { useState } from 'react';
import type { BotProfile } from '../api';
import { BotAvatar } from './BotAvatar';

export default function Sidebar({
  bots,
  activeBotId,
  schedulerActive,
  onSelectBot,
  onOpenScheduler,
  onOpenSettings,
  onCreateBot,
  onEditBot,
}: {
  bots: BotProfile[];
  activeBotId?: string;
  schedulerActive: boolean;
  onSelectBot: (id: string) => void;
  onOpenScheduler: () => void;
  onOpenSettings: () => void;
  onCreateBot: () => void;
  onEditBot: (bot: BotProfile) => void;
}) {
  const [collapsed, setCollapsed] = useState(() => {
    try { return localStorage.getItem('reeve-sidebar-collapsed') === '1'; }
    catch { return false; }
  });
  const toggleCollapsed = () => {
    const next = !collapsed;
    setCollapsed(next);
    try { localStorage.setItem('reeve-sidebar-collapsed', next ? '1' : '0'); }
    catch { /* The toggle still works when browser storage is unavailable. */ }
  };

  return (
    <aside
      className={`app-sidebar flex h-full shrink-0 flex-col border-r border-line bg-sidebar select-none text-fg transition-[width] duration-200 motion-reduce:transition-none ${collapsed ? 'w-16' : 'w-32 sm:w-64'}`}
      aria-label="侧栏"
    >
      <div className="min-h-0 flex-1 space-y-0.5 overflow-y-auto overflow-x-hidden px-2 pt-2 scrollbar-thin">
        <section aria-label="Bot 列表" className="mb-2">
          <header className="flex min-w-0 items-center py-0.5">
            {!collapsed && <span className="min-w-0 flex-1 truncate px-1 text-[13px] font-medium text-fg3">Bot</span>}
            <button type="button" disabled={!bots.length}
              onClick={onCreateBot}
              title={bots.length ? '新建 Bot' : '还没有可用的 Bot'}
              aria-label="新建 Bot"
              className="grid size-6 shrink-0 place-items-center rounded-md text-fg4 transition hover:bg-sidebar-hover hover:text-fg disabled:opacity-40">
              <Plus size={15} aria-hidden="true" />
            </button>
          </header>
          {!collapsed && !bots.length && <p className="px-2 py-1 text-xs text-fg4">还没有 Bot</p>}
          {bots.map((b) => {
            const active = !schedulerActive && activeBotId === b.id;
            return (
              <div key={b.id} className={`group flex items-center gap-0.5 rounded-2xl transition ${collapsed ? '' : 'pr-2'} ${
                active ? 'bg-sidebar-selected text-fg shadow-2xs' : 'text-fg2 hover:bg-sidebar-hover hover:text-fg'
              }`}>
                <button
                  type="button"
                  onClick={() => onSelectBot(b.id)}
                  title={b.name}
                  aria-label={b.name}
                  className={`flex min-w-0 flex-1 items-center gap-2 rounded-2xl px-2 py-2 text-left text-[14px] ${collapsed ? 'justify-center' : ''} ${active ? 'font-medium' : ''}`}
                  aria-current={active ? 'true' : undefined}
                >
                  <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={28} />
                  {!collapsed && <span className="truncate flex-1 tracking-tight">{b.name}</span>}
                </button>
                {!collapsed && <button type="button" onClick={() => onEditBot(b)} title={`编辑 ${b.name}`} aria-label={`编辑 ${b.name}`}
                  className={`grid size-6 shrink-0 place-items-center rounded-md hover:bg-sidebar-hover hover:text-fg ${active
                    ? 'text-fg3'
                    : 'text-fg4 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100'}`}>
                  <Pencil size={13} />
                </button>}
              </div>
            );
          })}
        </section>
      </div>

      <nav className="space-y-0.5 px-2 pb-3 pt-1" aria-label="应用导航">
        <button
          type="button"
          onClick={onOpenScheduler}
          disabled={!activeBotId}
          title="自动调度器"
          aria-label="自动调度器"
          aria-current={schedulerActive ? 'page' : undefined}
          className={`group flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] tracking-tight transition hover:text-fg ${collapsed ? 'h-10 justify-center' : ''} ${schedulerActive ? 'bg-sidebar-selected text-fg' : 'text-fg3 hover:bg-sidebar-hover'}`}
        >
          <History size={15} strokeWidth={1.7} aria-hidden="true" className="shrink-0 text-fg4 transition-colors group-hover:text-fg" />
          {!collapsed && <span>自动调度器</span>}
        </button>
        <button
          type="button"
          onClick={onOpenSettings}
          disabled={!activeBotId}
          className={`group flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] text-fg3 transition duration-150 hover:bg-sidebar-hover hover:text-fg disabled:opacity-40 ${collapsed ? 'h-10 justify-center' : ''}`}
          title="设置"
          aria-label="设置"
        >
          <SettingsIcon
            size={15}
            className="shrink-0 text-fg4 transition-colors duration-150 group-hover:text-fg"
            strokeWidth={1.7}
          />
          {!collapsed && <span className="font-normal tracking-tight">设置</span>}
        </button>
        <button type="button" onClick={toggleCollapsed} aria-expanded={!collapsed}
          title={collapsed ? '展开侧边栏' : '收起侧边栏'} aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
          className={`flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] text-fg3 transition hover:bg-sidebar-hover hover:text-fg ${collapsed ? 'h-10 justify-center' : ''}`}>
          <PanelLeft size={15} strokeWidth={1.7} className="shrink-0 text-fg4" aria-hidden="true" />
          {!collapsed && <span>收起侧边栏</span>}
        </button>
      </nav>
    </aside>
  );
}
