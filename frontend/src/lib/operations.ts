import {executingStates} from './task-state';
import {call,type Job,type Plan,type Row} from './api';
export class OperationRunner {
 private pending:{operation:string;signature:string;plan:Plan;key:string;job?:Job}|null=null;
 constructor(private report:(message:string)=>void,private changed:()=>Promise<unknown>,private pause:(ms:number)=>Promise<void>=ms=>new Promise(resolve=>setTimeout(resolve,ms))){}
 async discard(planID:string):Promise<void>{
  const job=await call<Job>('plan.discard',{id:planID});
  if(job.plan_id!==planID||job.state!=='cancelled')throw new Error('未收到原计划已放弃的确认，仍保留提交身份。');
  if(this.pending?.plan.id===planID)this.pending=null;
  await this.changed();
 }
 async resume(plan:Plan):Promise<void>{
  const key=this.pending?.plan.id===plan.id?this.pending.key:`recover-${plan.id}`;
  const job=await call<Job>(plan.operation==='host.dependencies.install'?'host.dependencies.confirm':'job.submit',{plan_id:plan.id,idempotency_key:key});
  if(!job.id||job.plan_id!==plan.id)throw new Error('未收到原计划的任务确认，仍保留提交身份。');
  if(this.pending?.plan.id===plan.id)this.pending=null;
  await this.changed();
 }
 async run(operation:string,input:object,label:string,wait=true):Promise<Job>{
  const signature=JSON.stringify(input);
  if(this.pending&&(this.pending.operation!==operation||this.pending.signature!==signature)){
   // A user may dismiss the retry banner while the accepted job finishes.
   // Reconcile read-only evidence; never resubmit an older mutation here.
   const records=await call<Row<Job>[]>('catalog.list',{kind:'job'});
   const matches=Array.isArray(records)?records.map(r=>r.data).filter(j=>j?.plan_id===this.pending!.plan.id):[];
   const job=matches.length===1?matches[0]:undefined;
   if(job?.id&&['succeeded','failed','cancelled'].includes(job.state)&&(!this.pending.job||this.pending.job.id===job.id))this.pending=null;
   if(this.pending)throw new Error('上次操作提交结果待确认，请先重试原操作。');
  }
  this.report(label);
  if(!this.pending){const plan=await call<Plan>('plan.create',{operation,input});this.pending={operation,signature,plan,key:crypto.randomUUID()};}
  const pending=this.pending;
  if(!pending.job)pending.job=await call<Job>('job.submit',{plan_id:pending.plan.id,idempotency_key:pending.key});
  await this.changed();
  if(!wait){this.pending=null;return pending.job}
  let job=pending.job,lastState='';
  while(executingStates.has(job.state)){
   if(job.state!==lastState){
    this.report(`${label} · ${job.state==='queued'?'等待执行':job.state==='cancel_requested'?'正在取消':'正在执行'}`);
    lastState=job.state;
   }
   await this.pause(500);job=(await call<Row<Job>>('catalog.get',{kind:'job',id:job.id})).data;pending.job=job;
  }
  await this.changed();
  this.pending=null;
  if(job.state!=='succeeded')throw new Error(job.error||`${label}未完成（${job.state}），已保留实际进度，请检查后重试。`);
  this.report(`${label} · 完成`);return job;
 }
}
