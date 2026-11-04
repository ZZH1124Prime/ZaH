import { describe,it,expect } from 'vitest';
import { bridge,call,rows } from './api';
describe('renderer boundary utilities',()=>{
 it('constructs a local Error from cloneable preload rejection without losing its code',async()=>{
  Object.defineProperty(window,'zah',{value:{call:async()=>{throw {message:'duplicate version',rpcCode:-32014}}},configurable:true});
  await expect(call('installation.configure')).rejects.toMatchObject({message:'duplicate version',rpcCode:-32014});
 });
 it('requires the desktop bridge rather than inventing data',()=>{Object.defineProperty(window,'zah',{value:undefined,writable:true,configurable:true});expect(()=>bridge()).toThrow('请通过 ZaH');});
 it('handles empty nullable SQLite lists',()=>{expect(rows(null)).toEqual([]);});
});
