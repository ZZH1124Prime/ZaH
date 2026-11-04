// Opt-in, real Go manager integration. No Docker/Bottles/application is started.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { watch } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { ManagerConnection } from '../main/manager';
import { CloseCoordinator } from '../main/close-coordinator';

async function main(): Promise<void> {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'zah-manager-test-'));
  const keys = ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR'];
  for (const key of keys) { const directory = path.join(root, key); await fs.mkdir(directory, {mode: 0o700}); process.env[key] = directory; }
  const socket = path.join(process.env.XDG_RUNTIME_DIR!, 'zah', 'manager.sock');
  const client = new ManagerConnection(path.resolve('bin/zah'));
  let shutdown = false;
  try {
    const [info, snapshot] = await Promise.all([client.call('system.info', {}), client.call('catalog.snapshot', {})]);
    assert.equal((info as {protocol: number}).protocol, 1);
    assert.deepEqual((snapshot as {family: unknown[] | null}).family ?? [], []);
    const family = await client.call('family.create', {name: '隔离联调测试'}) as {id: string};
    assert.ok(family.id);
    const fetched = await client.call('catalog.get', {kind: 'family', id: family.id}) as {data: {name: string}};
    assert.equal(fetched.data.name, '隔离联调测试');
    await assert.rejects(client.call('not.allowed', {}));
    await assert.rejects(client.call('release.create', {family_id: family.id, platform: 'invalid', arch: 'amd64', artifacts: []}));
    let confirmed=false,quit=false;const errors:string[]=[];
    const closing=new CloseCoordinator({call:(method,params)=>client.call(method,params),confirm:async()=>confirmed,report:async error=>{errors.push(error)},quit:()=>{quit=true}});
    await closing.request();assert.equal(quit,false);assert.deepEqual(await client.call('catalog.list',{kind:'job'}),[]);
    confirmed=true;await closing.request();assert.equal(quit,true);assert.deepEqual(errors,[]);
    console.log('PASS: production close coordinator cancels without jobs, then confirms an empty managed scope through real Go before authorizing quit');
    console.log('PASS: Electron-main client → real Go ready-fd/handshake → isolated SQLite round trip and typed failures');
  } finally {
    client.close();
    // Observe real socket removal when the idle manager exits, without killing
    // processes by name or leaving a detached manager against a deleted database.
    try {
      await new Promise<void>((resolve, reject) => {
        const watcher = watch(path.dirname(socket), async () => { try { await fs.lstat(socket); } catch (e) { if ((e as NodeJS.ErrnoException).code === 'ENOENT') finish(); } });
        const timer = setTimeout(() => finish(new Error('Isolated manager did not exit within idle budget')), 40_000);
        const finish = (error?: Error) => { clearTimeout(timer); watcher.close(); error ? reject(error) : resolve(); };
        void fs.lstat(socket).catch(() => finish());
      });
      shutdown = true;
    } finally { if (shutdown) await fs.rm(root, {recursive: true, force: true}); else console.error('Preserved isolated test data for diagnosis:', root); }
  }
  console.log('PASS: desktop disconnect leaves manager alive until its verified idle shutdown');
}
main().catch(error => { console.error(error instanceof Error ? error.message : 'Integration failed'); process.exitCode = 1; });
