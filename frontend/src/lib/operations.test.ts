import {beforeEach,it,expect,vi} from 'vitest';
import {OperationRunner} from './operations';
const invoke=vi.fn();
beforeEach(()=>{invoke.mockReset();Object.defineProperty(window,'zah',{value:{call:invoke},configurable:true})});
it('confirmed discard releases only the matching uncertain submission',async()=>{
 invoke.mockImplementation(async(m,p)=>{if(m==='plan.create')return{id:p.operation};if(m==='job.submit')throw new Error('unknown response');if(m==='plan.discard')return{id:'discard',plan_id:p.id,state:'cancelled'}});
 const runner=new OperationRunner(()=>{},async()=>{});
 await expect(runner.run('first',{},'first')).rejects.toThrow('unknown response');
 await runner.discard('unrelated');await expect(runner.run('second',{},'second')).rejects.toThrow('提交结果待确认');
 await runner.discard('first');await expect(runner.run('second',{},'second')).rejects.toThrow('unknown response');
 expect(invoke.mock.calls.filter(c=>c[0]==='plan.create')).toHaveLength(2);
});
it('failed discard retains the original identity; explicit resume uses the same key',async()=>{
 let submits=0;invoke.mockImplementation(async(m,p)=>{if(m==='plan.create')return{id:'p'};if(m==='job.submit'){if(++submits===1)throw new Error('lost');return{id:'j',plan_id:'p',state:'running'}}if(m==='plan.discard')throw new Error('already accepted')});
 const runner=new OperationRunner(()=>{},async()=>{});
 await expect(runner.run('instance.install',{},'install')).rejects.toThrow('lost');
 await expect(runner.discard('p')).rejects.toThrow('already accepted');
 await expect(runner.run('other',{},'other')).rejects.toThrow('提交结果待确认');
 await runner.resume({id:'p',operation:'instance.install',steps:[]});
 const calls=invoke.mock.calls.filter(c=>c[0]==='job.submit');expect(calls[0][1]).toEqual(calls[1][1]);
});
it('a dismissed retry does not lock unrelated actions once the original durable job is terminal',async()=>{
 let first=true;invoke.mockImplementation(async(m,p)=>{
  if(m==='plan.create')return{id:p.operation};
  if(m==='job.submit'){if(first){first=false;throw new Error('lost accepted response')}return{id:'next',plan_id:p.plan_id,state:'succeeded'}}
  if(m==='catalog.list')return[{data:{id:'original',plan_id:'old',state:'succeeded'}}];
 });
 const runner=new OperationRunner(()=>{},async()=>{});
 await expect(runner.run('old',{},'old')).rejects.toThrow('lost accepted response');
 await expect(runner.run('new',{},'new')).resolves.toMatchObject({id:'next'});
 expect(invoke.mock.calls.filter(c=>c[0]==='job.submit'&&c[1].plan_id==='old')).toHaveLength(1);
});
it('reuses the original submission identity after an uncertain response and rejects a different action',async()=>{
 let attempts=0;invoke.mockImplementation(async(m)=>{if(m==='plan.create')return {id:'p'};if(m==='job.submit'){if(attempts++===0)throw new Error('lost response');return {id:'j',state:'succeeded'}}});
 const runner=new OperationRunner(vi.fn(),async()=>{},async()=>{});
 await expect(runner.run('instance.stop',{id:'i'},'停止')).rejects.toThrow('lost response');
 await expect(runner.run('instance.launch',{id:'other'},'运行')).rejects.toThrow('提交结果待确认');
 await runner.run('instance.stop',{id:'i'},'停止');
 const submits=invoke.mock.calls.filter(c=>c[0]==='job.submit');expect(submits[0][1]).toEqual(submits[1][1]);expect(invoke.mock.calls.filter(c=>c[0]==='plan.create')).toHaveLength(1);
});
it('polls persisted job completion and reports failure without retrying installation',async()=>{
 invoke.mockImplementation(async(m)=>m==='plan.create'?{id:'p'}:m==='job.submit'?{id:'j',state:'running'}:{data:{id:'j',state:'failed',error:'installer failed'}});
 const runner=new OperationRunner(vi.fn(),async()=>{},async()=>{});await expect(runner.run('instance.install',{id:'i'},'安装')).rejects.toThrow('installer failed');expect(invoke.mock.calls.filter(c=>c[0]==='job.submit')).toHaveLength(1);
});
it('does not wait for a long-running launch worker when only submission is requested',async()=>{
 invoke.mockImplementation(async(m)=>m==='plan.create'?{id:'p'}:{id:'j',state:'running'});const runner=new OperationRunner(vi.fn(),async()=>{},async()=>{});await expect(runner.run('instance.launch',{id:'i'},'运行',false)).resolves.toMatchObject({state:'running'});expect(invoke.mock.calls.some(c=>c[0]==='catalog.get')).toBe(false);
});
it('retains the persisted job after a polling failure instead of submitting installation again',async()=>{
 let reads=0;const call=vi.fn(async(method:string)=>{if(method==='plan.create')return{id:'p'};if(method==='job.submit')return{id:'j',state:'running'};if(method==='catalog.get'){if(++reads===1)throw new Error('read interrupted');return{data:{id:'j',state:'succeeded'}}}throw new Error(method)});
 Object.defineProperty(window,'zah',{value:{call},configurable:true});const run=new OperationRunner(()=>{},async()=>{},async()=>{});
 await expect(run.run('instance.install',{id:'i'},'安装')).rejects.toThrow('read interrupted');await expect(run.run('instance.install',{id:'i'},'安装')).resolves.toMatchObject({id:'j',state:'succeeded'});
 expect(call.mock.calls.filter(c=>c[0]==='plan.create')).toHaveLength(1);expect(call.mock.calls.filter(c=>c[0]==='job.submit')).toHaveLength(1);
});

it('reports only state changes while polling, without reporting terminal jobs as running',async()=>{
 for(const terminal of ['succeeded','failed']){
  const states=['queued','running','running','cancel_requested','cancel_requested',terminal];
  invoke.mockImplementation(async(m)=>m==='plan.create'?{id:'p'}:m==='job.submit'?{id:'j',state:'queued'}:{data:{id:'j',state:states.shift(),error:terminal==='failed'?'fixture failure':undefined}});
  const report=vi.fn(),runner=new OperationRunner(report,async()=>{},async()=>{});
  const result=runner.run('instance.install',{id:'i'},'准备应用环境');
  if(terminal==='failed')await expect(result).rejects.toThrow('fixture failure');else await result;
  expect(report.mock.calls.map(c=>c[0])).toEqual(['准备应用环境','准备应用环境 · 等待执行','准备应用环境 · 正在执行','准备应用环境 · 正在取消',...(terminal==='succeeded'?['准备应用环境 · 完成']:[])]);
 }
});
