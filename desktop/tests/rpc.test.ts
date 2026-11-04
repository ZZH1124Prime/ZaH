import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import { RPCClient, ManagerRPCError, frame, MAX_FRAME } from '../main/rpc-client';

async function fixture(t: test.TestContext, respond: (socket: net.Socket, req: {id: number; method: string; params: unknown}) => void): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'zah-rpc-node-')); const socketPath = path.join(dir, 's'); const sockets = new Set<net.Socket>();
  const server = net.createServer(socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); let buffer = Buffer.alloc(0);
    socket.on('data', data => { buffer = Buffer.concat([buffer, data]); while (buffer.length >= 4) { const size = buffer.readUInt32BE(); if (buffer.length < size + 4) break;
      const req = JSON.parse(buffer.subarray(4, size + 4).toString()) as {id: number; method: string; params: unknown}; buffer = buffer.subarray(size + 4);
      if (req.method === 'protocol.hello') socket.write(frame({jsonrpc: '2.0', id: req.id, result: {version: 1, max_frame_bytes: MAX_FRAME}})); else respond(socket, req);
    }});
  });
  await new Promise<void>((resolve, reject) => {server.once('error', reject); server.listen(socketPath, resolve);});
  t.after(async () => { for (const socket of sockets) socket.destroy(); await new Promise<void>(resolve => server.close(() => resolve())); await fs.rm(dir, {recursive: true, force: true}); });
  return socketPath;
}
test('RPC client negotiates, handles fragmented replies and concurrent IDs', async t => {
  const socket = await fixture(t, (conn, req) => { const bytes = frame({jsonrpc: '2.0', id: req.id, result: req.params}); conn.write(bytes.subarray(0, 3)); conn.write(bytes.subarray(3)); });
  const client = new RPCClient(); t.after(() => client.close()); await client.connect(socket);
  const values = await Promise.all(Array.from({length: 8}, (_, n) => client.call('echo', {n}))); assert.deepEqual(values, Array.from({length: 8}, (_, n) => ({n})));
});
test('RPC client rejects oversize frames without replaying mutation', async t => {
  let calls = 0; const socket = await fixture(t, (conn) => {calls++; const header = Buffer.alloc(4); header.writeUInt32BE(MAX_FRAME + 1); conn.write(header);});
  const client = new RPCClient(); t.after(() => client.close()); await client.connect(socket);
  await assert.rejects(client.call('job.submit', {idempotency_key: 'test'}), /frame length/); assert.equal(calls, 1);
});
test('RPC client rejects unknown response IDs and surfaces safe Go errors', async t => {
  const socket = await fixture(t, (conn, req) => conn.write(req.method === 'fail' ? frame({jsonrpc: '2.0', id: req.id, error: {code: -32602, message: 'Invalid parameters'}}) : frame({jsonrpc: '2.0', id: req.id + 1, result: {}})));
  const client = new RPCClient(); t.after(() => client.close()); await client.connect(socket);
  await assert.rejects(client.call('fail', {}), /Invalid parameters/); await assert.rejects(client.call('bad', {}), /response ID/);
});
test('outgoing frames have big-endian byte length and enforce maximum', () => {
  const bytes = frame({text: '中文'}); assert.equal(bytes.readUInt32BE(), bytes.length - 4); assert.deepEqual(JSON.parse(bytes.subarray(4).toString()), {text: '中文'});
  assert.throws(() => frame({text: 'x'.repeat(MAX_FRAME)}));
});
test('definitive setup rejection keeps its code, unlike an unknown transport outcome',async t=>{
 const socket=await fixture(t,(conn,req)=>conn.write(frame({jsonrpc:'2.0',id:req.id,error:{code:-32014,message:'version already exists'}})));
 const client=new RPCClient();t.after(()=>client.close());await client.connect(socket);
 await assert.rejects(client.call('installation.configure',{}),error=>error instanceof ManagerRPCError&&error.rpcCode===-32014);
});
