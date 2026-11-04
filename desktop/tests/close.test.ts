import test from 'node:test';
import assert from 'node:assert/strict';
import { CloseCoordinator, verifyStopped, CLOSE_READ_METHODS } from '../main/close-coordinator';

const planID = 'a'.repeat(32), jobID = 'b'.repeat(32), instanceID = 'c'.repeat(32);
const plan = {id: planID, operation: 'applications.stopAll', input: {confirmed: true, targets: [{instance_id: instanceID}]}};
const succeeded = {id: jobID, plan_id: planID, state: 'succeeded', result: {failed: 0, stopped: 1, results: [{instance_id: instanceID, state: 'stopped'}]}};
function row<T extends {id: string}>(kind: string, data: T) { return {id: data.id, kind, data}; }
function harness(options: {confirm?: () => Promise<boolean>; job?: typeof succeeded; call?: (method: string, params: Record<string, unknown>) => Promise<unknown>; timeoutMs?: number; now?: () => number; mutationRevision?: () => number; drainWrites?: () => Promise<void>} = {}) {
  const calls: Array<{method: string; params: Record<string, unknown>}> = [], errors: string[] = [];
  let quits = 0, confirmations = 0;
  const invoke = async (method: string, params: Record<string, unknown>) => {
    calls.push({method, params});
    if (options.call) return options.call(method, params);
    if (method === 'plan.create') return plan;
    if (method === 'job.submit') return {id: jobID, plan_id: planID, state: 'queued'};
    return params.kind === 'plan' ? row('plan', plan) : row('job', options.job ?? succeeded);
  };
  const coordinator = new CloseCoordinator({call: invoke, confirm: async () => {confirmations++; return options.confirm ? options.confirm() : true;},
    report: async error => {errors.push(error);}, quit: () => {quits++;}, pause: async () => {}, timeoutMs: options.timeoutMs, now: options.now, mutationRevision: options.mutationRevision, drainWrites: options.drainWrites});
  return {coordinator, calls, errors, quits: () => quits, confirmations: () => confirmations};
}

test('cancelled window close keeps all applications and submits nothing', async () => {
  const h = harness({confirm: async () => false}); await h.coordinator.request();
  assert.equal(h.quits(), 0); assert.equal(h.calls.length, 0); assert.equal(h.coordinator.busy, false);
});
test('close requires persisted successful job and complete matching per-target evidence', async () => {
  const h = harness(); await h.coordinator.request();
  assert.equal(h.quits(), 1); assert.equal(h.coordinator.allowed, true); assert.deepEqual(h.errors, []);
  assert.deepEqual(h.calls.map(c => c.method), ['plan.create', 'job.submit', 'catalog.get', 'catalog.get']);
  assert.deepEqual(h.calls[0]!.params, {operation: 'applications.stopAll', input: {confirmed: true}});
});
test('failed or partially stopped jobs keep the window and permit a fresh confirmed retry', async () => {
  for (const job of [{...succeeded, state: 'failed'}, {...succeeded, result: {...succeeded.result, failed: 1}},
    {...succeeded, result: {...succeeded.result, results: []}}]) {
    const h = harness({job}); await h.coordinator.request(); await h.coordinator.request();
    assert.equal(h.quits(), 0); assert.equal(h.coordinator.allowed, false); assert.equal(h.errors.length, 2);
    assert.equal(h.calls.filter(c => c.method === 'plan.create').length, 2);
  }
});
test('unknown submission outcome retries the same durable plan and idempotency key', async () => {
  let submissions = 0;
  const h = harness({call: async (method, params) => {
    if (method === 'plan.create') return plan;
    if (method === 'job.submit') {if (++submissions === 1) throw new Error('connection lost');return {id: jobID, plan_id: planID};}
    return params.kind === 'plan' ? row('plan', plan) : row('job', succeeded);
  }});
  await h.coordinator.request(); assert.equal(h.quits(), 0);
  await h.coordinator.request(); assert.equal(h.quits(), 1);
  const submits = h.calls.filter(c => c.method === 'job.submit');assert.deepEqual(submits[0]!.params, submits[1]!.params);
  assert.equal(h.calls.filter(c => c.method === 'plan.create').length, 1);
});
test('repeated X while confirmation is open does not duplicate dialogs or jobs', async () => {
  let answer!: (value: boolean) => void;
  const h = harness({confirm: () => new Promise(resolve => {answer = resolve;})});
  const first = h.coordinator.request(); await h.coordinator.request(); assert.equal(h.confirmations(), 1);
  answer(true); await first; assert.equal(h.quits(), 1); assert.equal(h.calls.filter(c => c.method === 'job.submit').length, 1);
});
test('timeout keeps the running task and next close resumes polling without resubmitting', async () => {
  let finished = false, clock = 0;
  const h = harness({timeoutMs: 1, now: () => clock++, call: async (method, params) => {
    if (method === 'plan.create') return plan;
    if (method === 'job.submit') return {id: jobID, plan_id: planID};
    return params.kind === 'plan' ? row('plan', plan) : row('job', {...succeeded, state: finished ? 'succeeded' : 'running'});
  }});
  await h.coordinator.request(); assert.equal(h.quits(), 0); assert.equal(h.errors.length, 1);
  finished = true; await h.coordinator.request(); assert.equal(h.quits(), 1);
  assert.equal(h.calls.filter(c => c.method === 'job.submit').length, 1);
});
test('forged success, missing targets, duplicate targets and wrong operations never authorize exit', () => {
  for (const modified of [{...plan, operation: 'instance.launch'}, {...plan, input: {confirmed: false, targets: []}},
    {...plan, input: {confirmed: true, targets: [plan.input.targets[0], plan.input.targets[0]]}}])
    assert.throws(() => verifyStopped(modified, succeeded));
  for (const job of [{...succeeded, plan_id: 'd'.repeat(32)}, {...succeeded, state: 'running'},
    {...succeeded, result: {failed: 0, stopped: 1, results: [{instance_id: 'd'.repeat(32), state: 'stopped'}]}},
    {...succeeded, result: {failed: 0, stopped: 1, results: [{instance_id: instanceID, state: 'failed'}]}}])
    assert.throws(() => verifyStopped(plan, job));
  assert.doesNotThrow(() => verifyStopped({...plan, input: {confirmed: true, targets: []}}, {...succeeded, result: {failed: 0, stopped: 0, results: []}}));
});
test('close progress allows reads but never another launch or mutable catalog operation', () => {
  assert.ok(CLOSE_READ_METHODS.has('events.resume')); assert.ok(CLOSE_READ_METHODS.has('catalog.get'));
  for (const method of ['plan.create','job.submit','job.cancel','family.create','binding.register','catalog.delete']) assert.ok(!CLOSE_READ_METHODS.has(method));
});


test('close waits for accepted writes before freezing targets, and drain failure leaves window open', async () => {
  let drained = false;
  const h = harness({drainWrites: async () => {drained = true; throw new Error('write still pending');}});
  await h.coordinator.request(); assert.equal(drained, true); assert.equal(h.calls.length, 0); assert.equal(h.quits(), 0);
});
test('new writes after interrupted close require a fresh stop batch after resolving the old task', async () => {
  let revision = 0, reads = 0;
  const h = harness({mutationRevision: () => revision, call: async (method, params) => {
    if (method === 'plan.create') return plan;
    if (method === 'job.submit') return {id: jobID, plan_id: planID};
    if (++reads === 1) throw new Error('lost connection during poll');
    return params.kind === 'plan' ? row('plan', plan) : row('job', succeeded);
  }});
  await h.coordinator.request(); assert.equal(h.quits(), 0);
  revision++; await h.coordinator.request(); assert.equal(h.quits(), 1);
  const submissions = h.calls.filter(c => c.method === 'job.submit');
  assert.equal(submissions.length, 2); assert.notEqual(submissions[0]!.params.idempotency_key, submissions[1]!.params.idempotency_key);
  assert.equal(h.calls.filter(c => c.method === 'plan.create').length, 2);
});
