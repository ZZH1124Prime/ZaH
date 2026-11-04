import type { Job, Plan } from './api';

export const executingStates = new Set(['queued', 'running', 'cancel_requested']);
function stopTargets(plan: Plan): string[] {
 if (plan.operation === 'instance.stop') return plan.instance_id ? [plan.instance_id] : [];
 if (plan.operation !== 'applications.stopAll' || !plan.input || typeof plan.input !== 'object') return [];
 const targets = (plan.input as {targets?:unknown}).targets;
 if (!Array.isArray(targets)) return [];
 return targets.flatMap(value => value && typeof value === 'object' && typeof value.instance_id === 'string' ? [value.instance_id] : []);
}
/** Persisted jobs, including jobs created before this renderer session, govern UI locks. */
export function stopTaskState(jobs: Job[], plans: Plan[]) {
 const byID = new Map(plans.map(plan => [plan.id, plan]));
 const pending = new Set<string>(), failed = new Set<string>(), starting = new Set<string>();
 let batchPending = false;
 for (const job of [...jobs].sort((a,b) => a.created_at.localeCompare(b.created_at))) {
  const plan = byID.get(job.plan_id);
  if (plan?.operation==='instance.launch' && plan.instance_id && executingStates.has(job.state)) starting.add(plan.instance_id);
  if (!plan || !['instance.stop','applications.stopAll'].includes(plan.operation)) continue;
  const targets = stopTargets(plan);
  if (executingStates.has(job.state)) {
   if (plan.operation === 'applications.stopAll') batchPending = true;
   targets.forEach(id => pending.add(id));
  } else if (['failed','cancelled','interrupted'].includes(job.state)) targets.forEach(id => failed.add(id));
  else if (job.state === 'succeeded') {
   targets.forEach(id => failed.delete(id));
   if(plan.operation==='applications.stopAll'&&job.result&&typeof job.result==='object'){
    const results=(job.result as {results?:unknown}).results;
    if(Array.isArray(results))for(const value of results){if(value&&typeof value==='object'&&typeof value.instance_id==='string'&&targets.includes(value.instance_id)&&!['stopped','skipped'].includes(String(value.state)))failed.add(value.instance_id)}
   }
  }
 }
 pending.forEach(id => failed.delete(id));
 return {batchPending, pendingStartIDs:[...starting], pendingIDs:[...pending], failedIDs:[...failed]};
}
