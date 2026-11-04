import { execFile, spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';
import { Readable } from 'node:stream';
import { RPCClient } from './rpc-client';

const runFile = promisify(execFile);
class OutdatedManagerError extends Error {}

/** Do not forward Electron private library injection into the Go/backend tree. */
export function childEnvironment(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const clean = {...env};
  for (const key of ['LD_LIBRARY_PATH', 'LD_PRELOAD', 'LD_AUDIT', 'NODE_OPTIONS', 'ELECTRON_RUN_AS_NODE']) delete clean[key];
  for (const key of Object.keys(clean)) if (key.startsWith('DYLD_')) delete clean[key];
  return clean;
}

/** Go is the single authority for XDG resolution, including private fallback. */
export async function resolveSocket(binary: string): Promise<string> {
  const {stdout} = await runFile(binary, ['paths'], {timeout: 5000, maxBuffer: 64 * 1024, env: childEnvironment(process.env)});
  const result: unknown = JSON.parse(stdout);
  const runtime = (result as {Runtime?: unknown})?.Runtime;
  if (typeof runtime !== 'string' || !path.isAbsolute(runtime)) throw new Error('Manager returned invalid paths');
  const socket = path.join(runtime, 'manager.sock');
  if (Buffer.byteLength(socket) > 100) throw new Error('Configured runtime path exceeds Unix socket limit');
  return socket;
}

/** Node checks owned private endpoint paths; Go independently checks SO_PEERCRED. */
export async function validateEndpoint(socket: string): Promise<void> {
  const uid = process.getuid?.();
  if (uid === undefined) throw new Error('Linux desktop required');
  let parent = path.dirname(socket);
  while (true) {
    const part = await fs.lstat(parent);
    if (!part.isDirectory() || part.isSymbolicLink() || (part.uid !== 0 && part.uid !== uid) ||
      ((part.mode & 0o022) !== 0 && (part.mode & 0o1000) === 0)) throw new Error('Unsafe manager directory chain');
    const next = path.dirname(parent); if (next === parent) break; parent = next;
  }
  const dir = await fs.lstat(path.dirname(socket));
  const endpoint = await fs.lstat(socket);
  if (!dir.isDirectory() || dir.isSymbolicLink() || dir.uid !== uid || (dir.mode & 0o077) !== 0 ||
    !endpoint.isSocket() || endpoint.uid !== uid || (endpoint.mode & 0o077) !== 0) throw new Error('Unsafe manager endpoint');
}

/** Ready is a bounded newline-delimited message on a dedicated inherited fd. */
export async function waitForReady(stream: Readable, expectedSocket: string, timeoutMs = 10_000): Promise<void> {
  return new Promise((resolve, reject) => {
    let buffered = Buffer.alloc(0); let settled = false;
    const finish = (error?: Error) => { if (settled) return; settled = true; clearTimeout(timer); stream.off('data', onData); stream.off('end', onEnd); stream.off('error', onError); stream.destroy(); error ? reject(error) : resolve(); };
    const onEnd = () => finish(new Error('Manager ended before reporting readiness'));
    const onError = () => finish(new Error('Manager readiness pipe failed'));
    const onData = (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk]);
      if (buffered.length > 4096) { finish(new Error('Manager readiness message too large')); return; }
      const newline = buffered.indexOf(10); if (newline < 0) return;
      try { const msg = JSON.parse(buffered.subarray(0, newline).toString()) as {ready?: boolean;protocol?: number;socket?: string};
        if (msg.ready !== true || msg.protocol !== 1 || msg.socket !== expectedSocket) throw new Error();
        finish();
      } catch { finish(new Error('Invalid manager readiness message')); }
    };
    const timer = setTimeout(() => finish(new Error('Manager readiness timed out')), timeoutMs);
    stream.on('data', onData); stream.once('end', onEnd); stream.once('error', onError);
  });
}

/** Keeps the manager connection separate from the lifetime of its detached process. */
export class ManagerConnection {
  private client = new RPCClient();
  private connecting?: Promise<void>;
  private disposed = false;
  private startupFailure?: Error;
  private retryAfter = 0;
  private startupFailures = 0;
  /** Explicit user retry; automatic callers cannot create an unbounded spawn loop. */
  retryStartup(): void { this.startupFailure = undefined; this.retryAfter = 0; this.startupFailures = 0; }

  constructor(private readonly binary: string) {}
  async call(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (this.disposed) throw new Error('Desktop is closing');
    await this.ensure();
    if (this.disposed) { this.client.close(); throw new Error('Desktop is closing'); }
    // Never replay a request after send; mutations use durable Go idempotency keys.
    return this.client.call(method, params);
  }
  close(): void { this.disposed = true; this.client.close(); }
  private async connect(socket: string): Promise<void> {
    if (this.disposed) throw new Error('Desktop is closing');
    await validateEndpoint(socket);
    if (this.disposed) throw new Error('Desktop is closing');
    await this.client.connect(socket);
    try {
      const info=await this.client.call('system.info',{}) as {desktop_contract?:unknown};
      if(info?.desktop_contract!==7) throw new OutdatedManagerError('当前仍连接到旧版管理服务。请先用原窗口停止受管应用并关闭 ZaH，等待约 30 秒空闲退出后，再点击“重试连接”。不会自动结束已有管理服务。');
    } catch(error) {this.client.close();throw error}
    if (this.disposed) { this.client.close(); throw new Error('Desktop is closing'); }
  }
  private async ensure(): Promise<void> {
    if (this.connecting) return this.connecting;
    if (this.client.connected) return;
    if (this.startupFailure && (this.startupFailures >= 3 || Date.now() < this.retryAfter)) throw this.startupFailure;
    this.connecting = this.start();
    try { await this.connecting; this.startupFailure = undefined; this.startupFailures = 0; }
    catch (error) {
      this.startupFailures++;
      this.retryAfter = Date.now() + Math.min(30_000, 5000 * 2 ** (this.startupFailures - 1));
      this.startupFailure = error instanceof Error ? error : new Error('管理服务启动失败');
      throw this.startupFailure;
    } finally { this.connecting = undefined; }
  }
  private async start(): Promise<void> {
    const socket = await resolveSocket(this.binary);
    if (this.disposed) throw new Error('Desktop is closing');
    try { await this.connect(socket); return; } catch(error) { this.client.close(); if(error instanceof OutdatedManagerError)throw error; }
    if (this.disposed) throw new Error('Desktop is closing');
    const child = spawn(this.binary, ['manager', '--ready-fd', '3'], {
      detached: true, stdio: ['ignore', 'ignore', 'pipe', 'pipe'], env: childEnvironment(process.env)
    });
    let diagnostic = ''; let collecting = true;
    child.stderr?.on('data', (chunk: Buffer) => { if (collecting) diagnostic = (diagnostic + chunk.toString('utf8')).slice(-8192); });
    const exited = new Promise<string>(resolve => {
      child.once('close', (code, signal) => resolve(signal ? `signal ${signal}` : `exit ${code}`));
      child.once('error', () => resolve('spawn error'));
    });
    const ready = child.stdio[3];
    if (!(ready instanceof Readable)) throw new Error('Manager readiness pipe unavailable');
    const spawnError = new Promise<never>((_, reject) => child.once('error', () => reject(new Error('Cannot start Go manager'))));
    child.unref();
    try { await Promise.race([waitForReady(ready, socket), spawnError]); }
    catch (error) {
      // A concurrent CLI may have won the lock; prove readiness by a handshake.
      try { await this.connect(socket); collecting = false; return; } catch(connectionError) { if(connectionError instanceof OutdatedManagerError) {collecting=false;throw connectionError} }
      let timer: ReturnType<typeof setTimeout> | undefined;
      const status = await Promise.race([exited, new Promise<string>(resolve => { timer = setTimeout(() => resolve('readiness timeout; process state unconfirmed'), 250); })]);
      if (timer) clearTimeout(timer); collecting = false;
      const detail = diagnostic.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').trim().slice(-3000);
      throw new Error(`管理服务启动失败（${status}）：${detail || (error instanceof Error ? error.message : '未收到就绪消息')}。修正原因后点击“重试连接”。`);
    }
    collecting = false;
    if (this.disposed) throw new Error('Desktop is closing');
    await this.connect(socket);
  }
}
