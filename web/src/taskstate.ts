// task.updated 的合并规则抽成纯函数：界面状态机最容易漏的两种情况（暂停失败撤销、暂停原因清除）
// 放在这里，不依赖 React，可以直接用 node 跑断言。
export type TaskUpdate = {
  status?: string;
  prio?: number;
  error?: string;
  stop_reason?: string;
  clear_stop_reason?: boolean;
};

export type TaskMutable = {
  status: string;
  pausing?: boolean;
  stop_reason?: string;
  prio?: number;
  error?: string;
  ended?: number;
};

export const isTerminal = (st: string) => st === 'completed' || st === 'failed' || st === 'cancelled';

export function applyTaskUpdate(t: TaskMutable, d: TaskUpdate, at: number): void {
  if (d.status) {
    t.status = d.status;
    if (isTerminal(d.status)) t.ended = at;
    // queued/running 说明暂停请求已撤销或进程已恢复：别让界面永远停在「正在暂停」。
    if (d.status === 'queued' || d.status === 'running') t.pausing = false;
  }
  if (d.status === 'queued') t.stop_reason = undefined;
  if (d.clear_stop_reason) { t.stop_reason = undefined; t.pausing = false; }
  if (d.prio) t.prio = d.prio;
  if (d.error) t.error = d.error;
  if (d.stop_reason) t.stop_reason = d.stop_reason;
}
