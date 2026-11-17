// 执行器面板的脏检查抽成纯函数：白名单是有序数组，但语义是集合——顺序变化不算改动，
// 增删、默认执行器或任一项 CLI 选项变化才算。可以用 node --test 直接跑断言。
export type ExecutorOptionsDraft = { cli_model?: string; cli_auth?: string; cli_channel?: string };

export function execSignature(allowed: string[], def: string, clis: Record<string, ExecutorOptionsDraft>): string {
  const opts = Object.keys(clis).sort().map(id => [id, clis[id]?.cli_model ?? '', clis[id]?.cli_auth ?? '', clis[id]?.cli_channel ?? '']);
  return JSON.stringify([[...allowed].sort(), def, opts]);
}

// available 是三态：只有 probed=true 时才有结论。?probe=0 且没有缓存时后端返回
// available=false、probed=false——那不代表不可用，界面不能据此禁用执行器。
export type ExecutorProbe = { probed?: boolean; available?: boolean };

export function executorUnavailable(c: ExecutorProbe | undefined): boolean {
  return !!c && c.probed === true && c.available === false;
}

export function availabilityLabel(c: ExecutorProbe | undefined): '可用' | '不可用' | '未检测' {
  if (!c || c.probed !== true) return '未检测';
  return c.available === true ? '可用' : '不可用';
}
