import net from 'node:net';

export const MAX_FRAME = 1024 * 1024;
const MAX_PENDING = 32;
const TIMEOUT = 30_000;
export class ManagerRPCError extends Error {
  constructor(message:string,readonly rpcCode:number){super(message);this.name='ManagerRPCError';}
}
interface RPCResponse { jsonrpc: string; id: number; result?: unknown; error?: {code: number; message: string; data?: unknown} }
interface Pending { resolve(value: unknown): void; reject(error: Error): void; timer: NodeJS.Timeout }

/** Framing is shared by the Node socket client and headless protocol tests. */
export function frame(value: unknown): Buffer {
  const payload = Buffer.from(JSON.stringify(value), 'utf8');
  if (!payload.length || payload.length > MAX_FRAME) throw new Error('RPC frame exceeds limit');
  const header = Buffer.alloc(4); header.writeUInt32BE(payload.length);
  return Buffer.concat([header, payload]);
}

/** A sequential Go server may receive pipelined requests; responses use stable IDs. */
export class RPCClient {
  private socket?: net.Socket;
  private buffer = Buffer.alloc(0);
  private sequence = 0;
  private pending = new Map<number, Pending>();
  private ready = false;
  private connecting?: Promise<void>;
  get connected(): boolean { return this.ready && !!this.socket && !this.socket.destroyed; }

  async connect(socketPath: string): Promise<void> {
    if (this.connected) return;
    if (this.connecting) return this.connecting;
    this.connecting = this.open(socketPath);
    try { await this.connecting; } finally { this.connecting = undefined; }
  }
  private async open(socketPath: string): Promise<void> {
    this.ready = false;
    const socket = net.createConnection({path: socketPath}); this.socket = socket;
    socket.on('data', chunk => { if (this.socket === socket) this.receive(chunk); });
    socket.on('error', () => { if (this.socket === socket) this.fail(new Error('Local manager connection failed')); });
    socket.on('close', () => { if (this.socket === socket) this.fail(new Error('Local manager disconnected; submitted operations are not automatically retried')); });
    try { await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => { socket.destroy(); reject(new Error('Local manager connection timed out')); }, 2000);
      socket.once('connect', () => { clearTimeout(timer); resolve(); });
      socket.once('error', () => { clearTimeout(timer); reject(new Error('Local manager unavailable')); });
    });
    const hello = await this.call('protocol.hello', {version: 1}) as {version?: number; max_frame_bytes?: number};
    if (hello?.version !== 1 || hello.max_frame_bytes !== MAX_FRAME) throw new Error('Incompatible manager protocol');
    this.ready = true;
    } catch (error) { this.close(); throw error; }
  }

  call(method: string, params: Record<string, unknown>): Promise<unknown> {
    if (!this.socket || this.socket.destroyed || (!this.ready && method !== 'protocol.hello')) return Promise.reject(new Error('Local manager is disconnected'));
    if (this.pending.size >= MAX_PENDING) return Promise.reject(new Error('Too many pending requests'));
    const id = ++this.sequence;
    let message: Buffer;
    try { message = frame({jsonrpc: '2.0', id, method, params}); } catch (e) { return Promise.reject(e); }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.fail(new Error('Local request timed out; check task status before retrying')); }, TIMEOUT);
      this.pending.set(id, {resolve, reject, timer});
      this.socket!.write(message, error => { if (error) this.fail(new Error('Local request could not be sent')); });
    });
  }
  close(): void { this.fail(new Error('Desktop connection closed')); }
  private fail(error: Error): void {
    const socket = this.socket; this.socket = undefined; this.ready = false; this.buffer = Buffer.alloc(0);
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear(); socket?.destroy();
  }
  private receive(chunk: Buffer): void {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    while (this.buffer.length >= 4) {
      const length = this.buffer.readUInt32BE();
      if (!length || length > MAX_FRAME) { this.fail(new Error('Invalid manager frame length')); return; }
      if (this.buffer.length < length + 4) return;
      const payload = this.buffer.subarray(4, length + 4); this.buffer = this.buffer.subarray(length + 4);
      let response: RPCResponse;
      try { response = JSON.parse(payload.toString('utf8')) as RPCResponse; }
      catch { this.fail(new Error('Invalid manager response')); return; }
      const hasResult = Object.prototype.hasOwnProperty.call(response ?? {}, 'result');
      const hasError = Object.prototype.hasOwnProperty.call(response ?? {}, 'error');
      if (!response || response.jsonrpc !== '2.0' || !Number.isSafeInteger(response.id) || hasResult === hasError) {
        this.fail(new Error('Invalid manager response envelope')); return;
      }
      const pending = this.pending.get(response.id);
      if (!pending) { this.fail(new Error('Unexpected manager response ID')); return; }
      this.pending.delete(response.id); clearTimeout(pending.timer);
      if (hasError) {
        if (!response.error || typeof response.error.message !== 'string' || typeof response.error.code !== 'number') {
          pending.reject(new Error('Invalid manager error')); this.fail(new Error('Invalid manager response')); return;
        }
        pending.reject(new ManagerRPCError(response.error.message.slice(0, 4096),response.error.code));
      } else pending.resolve(response.result);
    }
  }
}
