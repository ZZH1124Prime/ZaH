import path from 'node:path';

export const APP_URL = 'zah://app/index.html';
export const MAX_IPC_BYTES = 256 * 1024;
export const METHODS = new Set([
  'host.dependencies.confirm', 'installation.programs', 'installation.inspect', 'installation.options', 'installation.configure', 'manager.retry', 'host.inspect', 'system.info', 'catalog.list', 'catalog.get', 'events.read', 'events.resume', 'catalog.snapshot',
  'catalog.rename', 'catalog.delete', 'family.create', 'release.create', 'release.icon.set', 'release.icon.get', 'plan.create', 'plan.discard', 'job.submit',
  'job.cancel', 'artifact.verify', 'bundle.inspect', 'recipe.add', 'instance.create', 'backend.assess',
  'instance.inspect', 'binding.register', 'binding.rebind', 'instance.preferences', 'capability.record', 'instance.desktopPlan'
]);

/** Only the installed main document, including its hash route, may use IPC. */
export function isAppDocument(value: string): boolean {
  try { const url = new URL(value); return url.protocol === 'zah:' && url.hostname === 'app' &&
    !url.username && !url.password && !url.port && url.pathname === '/index.html' && !url.search; }
  catch { return false; }
}

export function validateCall(method: unknown, params: unknown): asserts params is Record<string, unknown> {
  if (typeof method !== 'string' || !METHODS.has(method)) throw new Error('Unsupported operation');
  if (!params || typeof params !== 'object' || Array.isArray(params)) throw new Error('Parameters must be an object');
  const encoded = JSON.stringify(params);
  if (Buffer.byteLength(encoded) > MAX_IPC_BYTES) throw new Error('Request exceeds local IPC limit');
}

export const PLAN_OPERATIONS = new Set([
  'host.dependencies.install', 'installation.tools', 'installation.finish', 'installation.maintenance','artifact.import', 'artifact.remove', 'instance.install', 'instance.verify', 'instance.launch', 'instance.stop', 'instance.remove', 'instance.register',
  'bundle.import', 'bundle.restore', 'bundle.export', 'environment.rebuild', 'environment.export', 'environment.restore', 'environment.load', 'environment.verify', 'workbench.export', 'runtime.remove', 'instance.forget', 'instance.data.remove', 'bundle.encrypt', 'bundle.decrypt', 'applications.stopAll']);

type Grant = 'file' | 'directory' | 'save';
/** Paths are authorized by native dialogs, separately from untrusted renderer input. */
export class PathGrants {
  private grants = new Map<string, Set<Grant>>();
  add(value: string, kind: Grant): void {
    if (!path.isAbsolute(value) || value.includes('\0')) throw new Error('Invalid selected path');
    if (this.grants.size >= 2048 && !this.grants.has(value)) this.grants.delete(this.grants.keys().next().value!);
    const kinds = this.grants.get(value) ?? new Set<Grant>(); kinds.add(kind); this.grants.set(value, kinds);
  }
  clear(): void { this.grants.clear(); }
  private require(value: unknown, kind: Grant): void {
    if (typeof value !== 'string' || !this.grants.get(value)?.has(kind))
      throw new Error('Choose the path using the system dialog before this operation');
  }
  private workbench(input: Record<string, unknown>): void {
    if (Object.keys(input).some(key => !['path', 'cold_confirmed', 'instances', 'include_catalog'].includes(key)))
      throw new Error('Unknown workbench export field');
    this.require(input.path, 'save');
    if (input.include_catalog !== undefined && typeof input.include_catalog !== 'boolean') throw new Error('include_catalog must be a boolean');
    if (input.cold_confirmed !== true) throw new Error('Confirm all selected applications have stopped writing before export');
    if (!Array.isArray(input.instances) || input.instances.length < 1 || input.instances.length > 1000)
      throw new Error('Select between 1 and 1000 instances for workbench export');
    const ids = new Set<string>();
    for (const item of input.instances) {
      if (!item || typeof item !== 'object' || Array.isArray(item)) throw new Error('Invalid workbench instance');
      const value = item as Record<string, unknown>;
      if (Object.keys(value).some(key => !['id', 'bottles_archive_path', 'bottles_archive_sha256', 'include_bindings'].includes(key)))
        throw new Error('Unknown workbench instance field');
      if (typeof value.id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.id) || ids.has(value.id))
        throw new Error('Workbench instance IDs must be valid and unique');
      ids.add(value.id);
      validateBindingIDs(value.include_bindings);
      if (value.bottles_archive_path !== undefined) this.require(value.bottles_archive_path, 'file');
      if (value.bottles_archive_sha256 !== undefined) {
        if (typeof value.bottles_archive_sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(value.bottles_archive_sha256) || value.bottles_archive_path === undefined)
          throw new Error('Bottles archive digest must be SHA256 with a selected source file');
      }
    }
  }

  private crypt(operation: string, input: Record<string, unknown>): void {
    const encrypt = operation === 'bundle.encrypt';
    const fields = encrypt ? ['path', 'destination', 'recipients'] : ['path', 'destination', 'identity_path'];
    if (Object.keys(input).some(key => !fields.includes(key))) throw new Error('Unknown encryption field');
    this.require(input.path, 'file'); this.require(input.destination, 'save');
    if (typeof input.destination !== 'string' || !input.destination.endsWith(encrypt ? '.zahbundle.age' : '.zahbundle'))
      throw new Error('Select a destination with the expected bundle extension');
    if (input.path === input.destination) throw new Error('Source and destination must differ');
    if (encrypt) {
      const recipients = input.recipients;
      if (!Array.isArray(recipients) || recipients.length < 1 || recipients.length > 128 ||
        recipients.some(value => typeof value !== 'string' || value.length > 256 || !/^age1[023456789acdefghjklmnpqrstuvwxyz]{10,252}$/.test(value)) ||
        new Set(recipients).size !== recipients.length) throw new Error('Provide 1–128 unique age1 public recipient keys');
    } else this.require(input.identity_path, 'file');
  }

  private cleanup(operation: string, input: Record<string, unknown>): void {
    const runtime = operation === 'runtime.remove';
    const fields = runtime ? ['job_id', 'instance_id', 'external_quiet_confirmed'] :
      operation === 'instance.forget' ? ['id'] : ['id', 'cold_confirmed', 'confirm_private_data'];
    if (Object.keys(input).some(key => !fields.includes(key))) throw new Error('Unknown cleanup field');
    for (const field of runtime ? ['job_id', 'instance_id'] : ['id']) {
      if (typeof input[field] !== 'string' || !/^[a-f0-9]{32}$/.test(input[field])) throw new Error('Cleanup requires a persisted stable ID');
    }
    if (runtime && input.external_quiet_confirmed !== true) throw new Error('Confirm external runtime modifications have stopped');
    if (operation === 'instance.data.remove' && (input.cold_confirmed !== true || input.confirm_private_data !== true))
      throw new Error('Confirm writers have stopped and private application data may be permanently deleted');
  }

  validate(method: string, params: Record<string, unknown>): void {
    if(method==='job.submit' && Object.keys(params).some(k=>!['plan_id','idempotency_key'].includes(k)))throw new Error('Renderer cannot assert host installation approval');
    if(method==='host.dependencies.confirm' && (Object.keys(params).some(k=>!['plan_id','idempotency_key'].includes(k)) || typeof params.plan_id!=='string' || !/^[a-f0-9]{32}$/.test(params.plan_id)||typeof params.idempotency_key!=='string'|| !/^[A-Za-z0-9_-]{1,128}$/.test(params.idempotency_key)))throw new Error('Invalid dependency confirmation request');

    if (method === 'installation.programs' && (Object.keys(params).length!==1 || typeof params.id!=='string' || !/^[a-f0-9]{32}$/.test(params.id))) throw new Error('One registered instance ID required');
    if (method === 'installation.inspect' && (Object.keys(params).length!==1 || typeof params.artifact_id!=='string' || !/^[a-f0-9]{64}$/.test(params.artifact_id))) throw new Error('Inspect requires one archived material ID');
    if (method === 'installation.options' && (Object.keys(params).length!==1 || !['linux','windows'].includes(String(params.platform)))) throw new Error('Choose a supported platform');
    if (method === 'installation.configure') {
      const fields=['request_id','artifact_id','family_id','name','version','base','entry','mode','dpi'];
      if (Object.keys(params).some(k=>!fields.includes(k)) || typeof params.request_id!=='string' || !/^[a-f0-9]{32}$/.test(params.request_id) || typeof params.artifact_id!=='string' || !/^[a-f0-9]{64}$/.test(params.artifact_id)) throw new Error('Setup requires stable request and archived material IDs');
      if (params.family_id!==undefined && (typeof params.family_id!=='string' || !/^[a-f0-9]{32}$/.test(params.family_id))) throw new Error('Invalid application ID');
      if(params.dpi!==undefined&&(!Number.isInteger(params.dpi)||(params.dpi!==0&&((params.dpi as number)<96||(params.dpi as number)>384))))throw new Error('Invalid per-environment DPI');
      for(const key of ['name','version','base','entry','mode']) if(typeof params[key]!=='string' || (params[key] as string).length>4096) throw new Error('Invalid setup field');
    }
    if (method === 'manager.retry' && Object.keys(params).length !== 0) throw new Error('Reconnect accepts no parameters');
    if (method === 'events.resume') {
      if (Object.keys(params).some(key => key !== 'after' && key !== 'limit')) throw new Error('Unknown resume field');
      if (params.after !== undefined && params.after !== null && (!Number.isSafeInteger(params.after) || (params.after as number) < 0)) throw new Error('after must be a nonnegative safe integer or null');
      if (params.limit !== undefined && (!Number.isSafeInteger(params.limit) || (params.limit as number) < 1 || (params.limit as number) > 500)) throw new Error('limit must be an integer between 1 and 500');
    }
    if (method === 'plan.discard' && (Object.keys(params).length !== 1 || typeof params.id !== 'string' || !/^[a-f0-9]{32}$/.test(params.id)))
      throw new Error('Discard requires exactly one persisted plan ID');
    if (method === 'release.icon.set') this.require(params.path, 'file');
    if (method === 'bundle.inspect') this.require(params.path, 'file');
    if (method === 'binding.register' || method === 'binding.rebind') this.require(params.host_path, 'directory');
    if (method === 'plan.create') {
      if (typeof params.operation !== 'string' || !PLAN_OPERATIONS.has(params.operation)) throw new Error('Unsupported plan operation');
      const input = params.input;
      if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('Plan input is required');
      const value = input as Record<string, unknown>;
      if(params.operation==='environment.restore' && value.directory!==undefined)this.require(value.directory,'directory');
      if((params.operation==='environment.restore'||params.operation==='bundle.restore')&&value.parent!==undefined)throw new Error('Restore directory evidence is generated by the manager');
      if(params.operation==='installation.tools'&&(Object.keys(value).some(k=>!['id','action'].includes(k))||typeof value.id!=='string'||!/^[a-f0-9]{32}$/.test(value.id)||!['files','install','packages','uninstall','start'].includes(String(value.action))))throw new Error('Choose a fixed native environment tool');
      if (params.operation === 'installation.finish' || params.operation === 'installation.maintenance') {
        const allowed=params.operation==='installation.finish'?['id','executable_path','confirmed','kind','cwd','args']:['id','confirmed'];
        if(Object.keys(value).some(k=>!allowed.includes(k))||typeof value.id!=='string'||!/^[a-f0-9]{32}$/.test(value.id)||value.confirmed!==true) throw new Error('Entry change requires explicit confirmation and a registered instance');
        if(params.operation==='installation.finish') this.require(value.executable_path,'file');
        if(value.kind!==undefined&&!['desktop','executable'].includes(String(value.kind)))throw new Error('Unsupported entry kind');
        if(value.cwd!==undefined&&(typeof value.cwd!=='string'||value.cwd.length>4096||/[\0\r\n]/.test(value.cwd)))throw new Error('Invalid working directory');
        if(value.args!==undefined&&(!Array.isArray(value.args)||value.args.length>64||value.args.some(x=>typeof x!=='string'||x.length>8192||/[\0\r\n]/.test(x))))throw new Error('Invalid argument list');
      }
      if (params.operation === 'applications.stopAll' &&
        (Object.keys(params).some(key => key !== 'operation' && key !== 'input') ||
          Object.keys(value).length !== 1 || value.confirmed !== true))
        throw new Error('Stopping registered applications requires exactly confirmed: true');
      if (['instance.stop','instance.verify'].includes(String(params.operation)) && (Object.keys(value).length !== 1 || typeof value.id !== 'string' || !/^[a-f0-9]{32}$/.test(value.id))) throw new Error('Stop requires one registered instance ID');
      if (params.operation === 'instance.remove' && (Object.keys(value).some(k=>!['id','precheck_private_data'].includes(k)) || typeof value.id!=='string' || !/^[a-f0-9]{32}$/.test(value.id) || value.precheck_private_data!==true)) throw new Error('Management deletion requires private-data precheck');
      if (params.operation === 'workbench.export') this.workbench(value);
      if (params.operation === 'environment.export') validateBindingIDs(value.include_bindings);
      if (params.operation === 'bundle.encrypt' || params.operation === 'bundle.decrypt') this.crypt(params.operation, value);
      if (['runtime.remove', 'instance.forget', 'instance.data.remove'].includes(params.operation)) this.cleanup(params.operation, value);
      if (['artifact.import', 'bundle.import', 'bundle.restore', 'environment.restore'].includes(String(params.operation))) this.require(value.path, 'file');
      if (params.operation === 'instance.register') { this.require(value.bottle_path, 'directory'); this.require(value.executable_path, 'file'); }
      if (params.operation === 'bundle.export' || params.operation === 'environment.export') this.require(value.path, 'save');
      if (params.operation === 'environment.export' && value.bottles_archive_path !== undefined) this.require(value.bottles_archive_path, 'file');
    }
  }
}


/** Dialog formats are a closed set; renderer values never become arbitrary filters. */
export function bundleSuffix(format: unknown = 'plain'): '.zahbundle' | '.zahbundle.age' {
  if (format === 'plain') return '.zahbundle';
  if (format === 'encrypted') return '.zahbundle.age';
  throw new Error('Unsupported bundle save format');
}

function validateBindingIDs(value: unknown): void {
  if (value === undefined) return;
  if (!Array.isArray(value) || value.length > 128 ||
    value.some(id => typeof id !== 'string' || !/^[a-f0-9]{32}$/.test(id)) || new Set(value).size !== value.length)
    throw new Error('Shared content must use at most 128 unique registered binding IDs');
}
