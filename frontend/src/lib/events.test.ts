import { describe,it,expect,vi } from 'vitest';
import { CatalogEventReader,requestEvents,type ResumeEvents } from './events';
import type { Snapshot } from './api';
const snapshot:Snapshot={family:[],release:[],instance:[],environment:[],artifact:[],binding:[],capability:[],job:[]};
const initial:ResumeEvents={events:[],next_cursor:10,has_more:false,snapshot};
const event=(seq:number)=>({seq,type:'record.changed',data:{kind:'instance'},created_at:''});
describe('catalog event cursor recovery',()=>{
 it('applies the initial snapshot and consumes every page without skipping to a latest sequence',async()=>{
  const reader=new CatalogEventReader(),apply=vi.fn(async()=>{}),request=vi.fn().mockResolvedValueOnce(initial).mockResolvedValueOnce({events:[event(11)],next_cursor:11,has_more:true}).mockResolvedValueOnce({events:[event(12)],next_cursor:12,has_more:false});
  await reader.poll(request,apply);expect(apply).toHaveBeenCalledWith(snapshot);await reader.poll(request,apply);expect(request.mock.calls.map(c=>c[0])).toEqual([undefined,10,11]);expect(reader.after).toBe(12);expect(apply).toHaveBeenCalledTimes(3);
 });
 it('retains the previous cursor after refresh failure and does not refetch unchanged catalogs',async()=>{
  const reader=new CatalogEventReader();await reader.poll(async()=>initial,async()=>{});const page:ResumeEvents={events:[event(11)],next_cursor:11,has_more:false};
  await expect(reader.poll(async()=>page,async()=>{throw new Error('connection lost during refresh')})).rejects.toThrow('connection lost');expect(reader.after).toBe(10);
  const request=vi.fn(async()=>page),apply=vi.fn(async()=>{});await reader.poll(request,apply);expect(request).toHaveBeenCalledWith(10);expect(reader.after).toBe(11);
  apply.mockClear();await reader.poll(async()=>({events:[],next_cursor:11,has_more:false}),apply);expect(apply).not.toHaveBeenCalled();
 });
 it('rejects an event page that skips beyond its last event and accepts a server reset snapshot',async()=>{
  const reader=new CatalogEventReader();await reader.poll(async()=>initial,async()=>{});
  await expect(reader.poll(async()=>({events:[event(11)],next_cursor:90,has_more:false}),async()=>{})).rejects.toThrow('游标不一致');expect(reader.after).toBe(10);
  const apply=vi.fn(async()=>{});await reader.poll(async()=>({...initial,next_cursor:2}),apply);expect(reader.after).toBe(2);expect(apply).toHaveBeenCalledWith(snapshot);
 });
 it('serializes repeated reconnects and only invokes the read RPC',async()=>{
  const reader=new CatalogEventReader();let resolve!:(value:ResumeEvents)=>void;const pending=new Promise<ResumeEvents>(done=>{resolve=done});const invoke=vi.fn(async(_method:string,_params:object)=>pending);Object.defineProperty(window,'zah',{configurable:true,writable:true,value:{call:invoke}});
  const apply=vi.fn(async()=>{}),first=reader.poll(requestEvents,apply),second=reader.poll(requestEvents,apply);expect(first).toBe(second);expect(invoke).toHaveBeenCalledTimes(1);expect(invoke).toHaveBeenCalledWith('events.resume',{limit:100});resolve(initial);await Promise.all([first,second]);expect(apply).toHaveBeenCalledTimes(1);expect(invoke.mock.calls.every(c=>c[0]==='events.resume')).toBe(true);
 });
});
