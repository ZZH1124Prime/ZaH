import { it, expect } from 'vitest';
import { stopTaskState } from './task-state';
import type { Job, Plan } from './api';
const one:Plan={id:'one',operation:'instance.stop',instance_id:'i',steps:[]};
const batch:Plan={id:'batch',operation:'applications.stopAll',input:{targets:[{instance_id:'i'},{instance_id:'other'}]},steps:[]};
const job=(id:string,plan_id:string,state:string,created_at='2026-11-01'):Job=>({id,plan_id,state,created_at,updated_at:created_at,step_index:0});
it('recovers a persisted batch lock and all target identities after reload',()=>{
 expect(stopTaskState([job('j','batch','queued')],[batch])).toEqual({batchPending:true,pendingStartIDs:[],pendingIDs:['i','other'],failedIDs:[]});
});
it('active stop evidence overrides prior failures without clearing backend state',()=>{
 const result=stopTaskState([job('old','one','failed'),job('new','batch','running','2026-11-02')],[one,batch]);
 expect(result.failedIDs).toEqual([]);expect(result.pendingIDs).toContain('i');
});
it('failed and interrupted stops become unknown/retry candidates, successful later stops clear them',()=>{
 expect(stopTaskState([job('old','one','failed')],[one]).failedIDs).toEqual(['i']);
 expect(stopTaskState([job('old','one','interrupted')],[one]).failedIDs).toEqual(['i']);
 expect(stopTaskState([job('old','one','failed'),job('new','one','succeeded','2026-11-02')],[one]).failedIDs).toEqual([]);
});
it('keeps individual failures retryable when the batch itself completed successfully',()=>{
 const completed={...job('j','batch','succeeded'),result:{results:[{instance_id:'i',state:'failed',error:'backend unavailable'},{instance_id:'other',state:'stopped'}],failed:1,stopped:1}};
 expect(stopTaskState([completed],[batch])).toEqual({batchPending:false,pendingStartIDs:[],pendingIDs:[],failedIDs:['i']});
});
it('an already absent or inactive skipped target counts as confirmed stopped',()=>{
 const completed={...job('j','batch','succeeded'),result:{results:[{instance_id:'i',state:'skipped'},{instance_id:'other',state:'stopped'}],failed:0,stopped:2}};
 expect(stopTaskState([completed],[batch]).failedIDs).toEqual([]);
});
