import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {ManagerConnection} from '../main/manager';

test('startup preserves bounded stderr and exit status, coalesces callers and requires explicit retry during cooldown',async()=>{
 const root=await fs.mkdtemp(path.join(os.tmpdir(),'zah-startup-'));
 const binary=path.join(root,'fake-manager');const count=path.join(root,'attempts');
 const manager=new ManagerConnection(binary);
 try {
  const script=`#!/usr/bin/env node\nconst fs=require('node:fs');if(process.argv[2]==='paths'){console.log(JSON.stringify({Runtime:${JSON.stringify(root)}}));process.exit(0)}fs.appendFileSync(${JSON.stringify(count)},'x');process.stderr.write('a'.repeat(10000)+'\\nfixture: writable ancestor rejected');process.exitCode=7;`;
  await fs.writeFile(binary,script,{mode:0o700});
  const errors=await Promise.all(Array.from({length:30},()=>manager.call('system.info',{}).then(()=>'',e=>String(e.message))));
  for(const error of errors){assert.match(error,/exit 7/);assert.match(error,/fixture: writable ancestor rejected/);assert.ok(error.length<4500)}
  assert.equal(await fs.readFile(count,'utf8'),'x');
  await assert.rejects(manager.call('system.info',{}),/fixture:/);
  assert.equal(await fs.readFile(count,'utf8'),'x');
  manager.retryStartup();await assert.rejects(manager.call('system.info',{}),/exit 7/);
  assert.equal(await fs.readFile(count,'utf8'),'xx');
 } finally {manager.close();await fs.rm(root,{recursive:true,force:true})}
});

test('incompatible existing manager is reported without spawning or terminating it',async()=>{
 const net=await import('node:net');const {frame,MAX_FRAME}=await import('../main/rpc-client.js');
 const root=await fs.mkdtemp(path.join(os.tmpdir(),'zah-old-manager-'));const socketPath=path.join(root,'manager.sock');const attempts=path.join(root,'spawned');const binary=path.join(root,'fake-binary');let contract=1;
 const sockets=new Set<import('node:net').Socket>();const server=net.createServer(socket=>{sockets.add(socket);socket.on('close',()=>sockets.delete(socket));let data=Buffer.alloc(0);socket.on('data',chunk=>{data=Buffer.concat([data,chunk]);while(data.length>=4&&data.length>=data.readUInt32BE()+4){const n=data.readUInt32BE();const req=JSON.parse(data.subarray(4,n+4).toString());data=data.subarray(n+4);socket.write(frame({jsonrpc:'2.0',id:req.id,result:req.method==='protocol.hello'?{version:1,max_frame_bytes:MAX_FRAME}:{protocol:1,desktop_contract:contract}}))}})});
 const manager=new ManagerConnection(binary);
 try {
  await new Promise<void>((resolve,reject)=>{server.once('error',reject);server.listen(socketPath,resolve)});await fs.chmod(socketPath,0o600);
  await fs.writeFile(binary,`#!/usr/bin/env node\nconst fs=require('node:fs');if(process.argv[2]==='paths')console.log(JSON.stringify({Runtime:${JSON.stringify(root)}}));else fs.writeFileSync(${JSON.stringify(attempts)},'unexpected');`,{mode:0o700});
  await assert.rejects(manager.call('system.info',{}),/旧版管理服务/);assert.equal(server.listening,true);await assert.rejects(fs.stat(attempts),{code:'ENOENT'});
  contract=7;manager.retryStartup();assert.deepEqual(await manager.call('system.info',{}),{protocol:1,desktop_contract:7});await assert.rejects(fs.stat(attempts),{code:'ENOENT'});
 }finally{manager.close();for(const socket of sockets)socket.destroy();await new Promise<void>(resolve=>server.close(()=>resolve()));await fs.rm(root,{recursive:true,force:true})}
});
