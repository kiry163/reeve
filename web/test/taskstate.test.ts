import assert from 'node:assert/strict';
import { test } from 'node:test';
import { applyTaskUpdate } from '../src/taskstate.ts';

const base = () => ({ status: 'running', pausing: false, stop_reason: undefined as string | undefined, prio: 1, error: undefined as string | undefined });
const AT = 1700000000000;

test('clear_stop_reason 撤掉暂停标记与原因（暂停失败/恢复）', () => {
  const t = base();
  t.pausing = true;
  t.stop_reason = 'user';
  applyTaskUpdate(t, { status: 'running', clear_stop_reason: true }, AT);
  assert.equal(t.pausing, false);
  assert.equal(t.stop_reason, undefined);
  assert.equal(t.status, 'running');
});

test('running 即使不带清除标记也要撤掉 pausing', () => {
  const t = base();
  t.pausing = true;
  applyTaskUpdate(t, { status: 'running' }, AT);
  assert.equal(t.pausing, false);
});

test('暂停保留 stop_reason，队列清原因，终态写 ended', () => {
  const t = base();
  applyTaskUpdate(t, { status: 'paused', stop_reason: 'stall' }, AT);
  assert.equal(t.status, 'paused');
  assert.equal(t.stop_reason, 'stall');

  const q = base();
  q.stop_reason = 'user';
  applyTaskUpdate(q, { status: 'queued' }, AT);
  assert.equal(q.stop_reason, undefined);
  assert.equal(q.ended, undefined);

  applyTaskUpdate(q, { status: 'failed', error: 'boom' }, AT);
  assert.equal(q.ended, AT);
  assert.equal(q.error, 'boom');
});

test('interrupted 能合并进暂停状态（WorkingCard 据此显示「重新执行」）', () => {
  const t = base();
  applyTaskUpdate(t, { status: 'paused', stop_reason: 'interrupted' }, AT);
  assert.equal(t.status, 'paused');
  assert.equal(t.stop_reason, 'interrupted');
});
