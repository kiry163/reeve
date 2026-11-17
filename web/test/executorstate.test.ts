import assert from 'node:assert/strict';
import { test } from 'node:test';
import { availabilityLabel, execSignature, executorUnavailable } from '../src/executorstate.ts';

const saved = { omp: { cli_auth: 'subscription' } };

test('白名单顺序变化不算改动', () => {
  assert.equal(
    execSignature(['builtin', 'codex', 'omp'], 'builtin', saved),
    execSignature(['builtin', 'omp', 'codex'], 'builtin', saved),
  );
});

test('白名单增删算改动', () => {
  assert.notEqual(
    execSignature(['builtin', 'codex', 'omp'], 'builtin', saved),
    execSignature(['builtin', 'omp'], 'builtin', saved),
  );
  assert.notEqual(
    execSignature(['builtin', 'omp'], 'builtin', saved),
    execSignature(['builtin', 'omp', 'pi'], 'builtin', saved),
  );
});

test('默认执行器与 CLI 选项变化算改动', () => {
  assert.notEqual(
    execSignature(['builtin', 'omp'], 'builtin', saved),
    execSignature(['builtin', 'omp'], 'omp', saved),
  );
  assert.notEqual(
    execSignature(['builtin', 'omp'], 'builtin', saved),
    execSignature(['builtin', 'omp'], 'builtin', {}),
  );
});

test('未探测时 available=false 不算不可用', () => {
  assert.equal(executorUnavailable({ probed: false, available: false }), false);
  assert.equal(executorUnavailable({ available: false }), false);
  assert.equal(availabilityLabel({ probed: false, available: false }), '未检测');
});

test('探测过才给出结论', () => {
  assert.equal(executorUnavailable({ probed: true, available: false }), true);
  assert.equal(executorUnavailable({ probed: true, available: true }), false);
  assert.equal(availabilityLabel({ probed: true, available: true }), '可用');
  assert.equal(availabilityLabel({ probed: true, available: false }), '不可用');
  assert.equal(executorUnavailable(undefined), false);
});
