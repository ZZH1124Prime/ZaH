/** Main-process gate. The renderer can request review but cannot assert approval. */
export class DependencyConfirmation {
 private pending=new Map<string,Promise<unknown>>();
 private approved=new Set<string>();
 constructor(private call:(method:string,params:Record<string,unknown>)=>Promise<unknown>,private confirm:(detail:string)=>Promise<boolean>){}
 run(planID:string,key:string):Promise<unknown>{
  const pending=this.pending.get(planID);if(pending)return pending;
  const work=this.review(planID,key).finally(()=>this.pending.delete(planID));this.pending.set(planID,work);return work;
 }
 private async review(planID:string,key:string):Promise<unknown>{
  const row=await this.call('catalog.get',{kind:'plan',id:planID}) as {data?:{operation?:string;input?:{distribution?:string;packages?:unknown};data_impact?:unknown}};
  const p=row?.data;if(p?.operation!=='host.dependencies.install'||!Array.isArray(p.input?.packages)||p.input.packages.length<1||p.input.packages.length>256||!p.input.packages.every(x=>typeof x==='string'&&/^[a-z0-9][a-z0-9+.:_-]*=[A-Za-z0-9.+:~_-]+$/.test(x)))throw new Error('依赖安装计划无效，请重新检查。');
  if(!this.approved.has(planID)){
   const notes=Array.isArray(p.data_impact)?p.data_impact.filter(x=>typeof x==='string').join('\n'):'';
   if(!await this.confirm(`${p.input.distribution??''}\n\n${p.input.packages.join('\n')}\n\n${notes}`))return {cancelled:true};
   if(this.approved.size>=256)this.approved.delete(this.approved.values().next().value!);
   this.approved.add(planID);
  }
  return this.call('job.submit',{plan_id:planID,idempotency_key:key,host_changes_confirmed:true});
 }
}
