import type { Event, EventPage, ResumeParams } from '../../../schemas/rpc.generated';
import { call, type Snapshot } from './api';

export type CatalogEvent=Event;
export interface ResumeEvents extends Omit<EventPage,'snapshot'>{snapshot?:Snapshot}
type ResumeRequest=(after:number|undefined)=>Promise<ResumeEvents>;

/** A cursor acknowledges applied reads only. It never submits/replays mutations. */
export class CatalogEventReader {
 private cursor:number|undefined;
 private inFlight:Promise<number>|undefined;
 get after(){return this.cursor;}
 poll(request:ResumeRequest,apply:(snapshot:Snapshot|undefined)=>Promise<void>):Promise<number>{
  if(this.inFlight)return this.inFlight;
  this.inFlight=this.consume(request,apply).finally(()=>{this.inFlight=undefined});
  return this.inFlight;
 }
 private async consume(request:ResumeRequest,apply:(snapshot:Snapshot|undefined)=>Promise<void>):Promise<number>{
  // Yield between large backlogs; next poll starts at the last applied page.
  for(let page=0;page<16;page++){
   const result=await request(this.cursor);
   if(!result||!Array.isArray(result.events)||!Number.isSafeInteger(result.next_cursor)||result.next_cursor<0||typeof result.has_more!=='boolean')throw new Error('管理服务返回了无效的事件游标。');
   if(result.snapshot!==undefined){
    if(!result.snapshot||typeof result.snapshot!=='object'||Array.isArray(result.snapshot))throw new Error('管理服务返回了无效的恢复快照。');
   }else{
    if(this.cursor===undefined||result.next_cursor<this.cursor)throw new Error('事件同步需要完整快照恢复。');
    let prior=this.cursor;
    for(const event of result.events){if(!event||!Number.isSafeInteger(event.seq)||event.seq<=prior)throw new Error('管理服务返回了无序的事件页。');prior=event.seq;}
    if(result.next_cursor!==prior||(result.has_more&&result.events.length===0))throw new Error('事件页与续读游标不一致。');
   }
   if(result.snapshot!==undefined||result.events.length>0)await apply(result.snapshot);
   // Failed reads/cache refreshes leave this page unacknowledged for reconnect.
   this.cursor=result.next_cursor;
   if(!result.has_more)return this.cursor;
  }
  return this.cursor!;
 }
}

export function requestEvents(after:number|undefined):Promise<ResumeEvents>{const params:ResumeParams={...(after===undefined?{}:{after}),limit:100};return call<ResumeEvents>('events.resume',params);}
