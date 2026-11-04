import { randomUUID } from 'node:crypto';

export interface CloseDependencies {
  call(method: string, params: Record<string, unknown>): Promise<unknown>;
  confirm(): Promise<boolean>;
  drainWrites?: () => Promise<void>;
  mutationRevision?: () => number;
  report(error: string): Promise<void>;
  quit(): void;
  pause?: () => Promise<void>;
  now?: () => number;
  timeoutMs?: number;
}
interface PendingStop { planID: string; key: string; jobID?: string; revision: number }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('停止任务返回了无效证据。');
  return value as Record<string, unknown>;
}
function stableID(value: unknown): string {
  if (typeof value !== 'string' || !/^[a-f0-9]{32}$/.test(value)) throw new Error('停止任务缺少有效的持久标识。');
  return value;
}
function record(value: unknown, kind: string, id: string): Record<string, unknown> {
  const row = object(value);
  if (row.kind !== kind || row.id !== id) throw new Error('停止任务记录与请求不一致。');
  const data = object(row.data);
  if (data.id !== id) throw new Error('停止任务内容标识不一致。');
  return data;
}
export function verifyStopped(plan: Record<string, unknown>, job: Record<string, unknown>): void {
  if (plan.operation !== 'applications.stopAll' || job.plan_id !== plan.id || job.state !== 'succeeded')
    throw new Error('尚未确认全部停止成功。');
  const input = object(plan.input), result = object(job.result);
  if (input.confirmed !== true || !Array.isArray(input.targets) || input.targets.length > 256 || !Array.isArray(result.results))
    throw new Error('停止结果缺少逐项身份核验。');
  const targets = new Set(input.targets.map(value => stableID(object(value).instance_id)));
  if (targets.size !== input.targets.length || result.failed !== 0 || result.stopped !== targets.size || result.results.length !== targets.size)
    throw new Error('部分应用未能确认停止，窗口将保持打开。');
  for (const value of result.results) {
    const item = object(value), id = stableID(item.instance_id);
    if (!targets.delete(id) || !['stopped', 'skipped'].includes(String(item.state)) || item.error)
      throw new Error('部分应用未能确认停止，窗口将保持打开。');
  }
  if (targets.size) throw new Error('停止结果不完整，窗口将保持打开。');
}

/** Owns close confirmation and durable submission; renderer cannot authorize quit. */
export class CloseCoordinator {
  busy = false;
  allowed = false;
  private pending?: PendingStop;
  constructor(private readonly deps: CloseDependencies) {}
  async request(): Promise<void> {
    if (this.busy || this.allowed) return;
    this.busy = true;
    try {
      if (!await this.deps.confirm()) return;
      await this.deps.drainWrites?.();
      await this.stopConfirmed();
    } catch (error) {
      await this.deps.report(error instanceof Error ? error.message.slice(0, 4096) : '无法核实全部停止，窗口保持打开。').catch(() => undefined);
    } finally { this.busy = false; }
  }
  private async stopConfirmed(): Promise<void> {
    if (!this.pending) {
      const plan = object(await this.deps.call('plan.create', {operation: 'applications.stopAll', input: {confirmed: true}}));
      if (plan.operation !== 'applications.stopAll') throw new Error('服务返回了错误的停止计划。');
      this.pending = {planID: stableID(plan.id), key: randomUUID(), revision: this.deps.mutationRevision?.() ?? 0};
    }
    const pending = this.pending;
    if (!pending.jobID) {
      const job = object(await this.deps.call('job.submit', {plan_id: pending.planID, idempotency_key: pending.key}));
      if (job.plan_id !== pending.planID) throw new Error('服务返回了不匹配的停止任务。');
      pending.jobID = stableID(job.id);
    }
    const now = this.deps.now ?? Date.now;
    const deadline = now() + (this.deps.timeoutMs ?? 120_000);
    while (true) {
      const job = record(await this.deps.call('catalog.get', {kind: 'job', id: pending.jobID}), 'job', pending.jobID);
      if (job.plan_id !== pending.planID) throw new Error('停止任务关联计划不匹配。');
      if (job.state === 'succeeded') {
        const plan = record(await this.deps.call('catalog.get', {kind: 'plan', id: pending.planID}), 'plan', pending.planID);
        try { verifyStopped(plan, job); } catch (error) { this.pending = undefined; throw error; }
        if (pending.revision !== (this.deps.mutationRevision?.() ?? 0)) {
          // After an interrupted close, newer UI writes may have launched a
          // new environment. Resolve the original task before freezing a new
          // batch under the user's current confirmation.
          this.pending = undefined;
          await this.stopConfirmed(); return;
        }
        this.allowed = true; this.deps.quit(); return;
      }
      if (!['queued', 'running', 'cancel_requested'].includes(String(job.state))) {
        this.pending = undefined;
        throw new Error('全部停止未成功。窗口保持打开，请查看任务中的逐项结果，再重试关闭。');
      }
      if (now() >= deadline) throw new Error('停止任务尚未完成。窗口保持打开，可查看任务；再次关闭会继续核实原任务。');
      await (this.deps.pause ? this.deps.pause() : new Promise<void>(resolve => setTimeout(resolve, 500)));
    }
  }
}

// Reads remain available so task progress can still be inspected during close.
export const CLOSE_READ_METHODS = new Set(['host.inspect', 'system.info', 'catalog.list', 'catalog.get',
  'events.read', 'events.resume', 'catalog.snapshot', 'release.icon.get', 'artifact.verify', 'bundle.inspect', 'backend.assess', 'instance.inspect']);
