import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { PassThrough } from 'node:stream';
import { APP_URL, isAppDocument, PathGrants, validateCall, METHODS, PLAN_OPERATIONS, bundleSuffix } from '../main/security';
import { childEnvironment, waitForReady, validateEndpoint } from '../main/manager';
import { resolveAsset, staticResponse } from '../protocol/static';

test('IPC permits only main application document and explicit methods', () => {
  assert.ok(isAppDocument(APP_URL)); assert.ok(isAppDocument(APP_URL + '#/library'));
  for (const url of ['https://app/index.html', 'zah://app.evil/index.html', 'zah://user@app/index.html', 'zah://app/other.html', APP_URL + '?remote=1', 'file:///index.html']) assert.equal(isAppDocument(url), false);
  assert.doesNotThrow(() => validateCall('catalog.snapshot', {}));
  for (const method of ['shell', 'fs.read', 'protocol.hello', 'manager.stop', '']) assert.throws(() => validateCall(method, {}));
  assert.throws(() => validateCall('catalog.list', []));
  assert.throws(() => validateCall('catalog.list', {payload: 'x'.repeat(300_000)}));
});
test('file grants cannot authorize other paths or export writes', () => {
  const grants = new PathGrants(); grants.add('/tmp/selected.exe', 'file');
  assert.doesNotThrow(() => grants.validate('plan.create', {operation: 'artifact.import', input: {path: '/tmp/selected.exe'}}));
  assert.throws(() => grants.validate('plan.create', {operation: 'artifact.import', input: {path: '/etc/passwd'}}));
  assert.throws(() => grants.validate('plan.create', {operation: 'bundle.export', input: {path: '/tmp/selected.exe'}}));
  grants.add('/tmp/out.zahbundle', 'save');
  assert.doesNotThrow(() => grants.validate('plan.create', {operation: 'bundle.export', input: {path: '/tmp/out.zahbundle'}}));
  assert.throws(() => grants.validate('binding.rebind', {host_path: '/tmp/out.zahbundle'}));
  grants.clear(); assert.throws(() => grants.validate('bundle.inspect', {path: '/tmp/selected.exe'}));
});
test('static protocol rejects traversal, symlink escape, unsupported types and remote URL', async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'zah-static-')); t.after(() => fs.rm(dir, {recursive: true, force: true}));
  const root = path.join(dir, 'app'); await fs.mkdir(root); await fs.writeFile(path.join(root, 'index.html'), '<h1>ZaH</h1>');
  await fs.writeFile(path.join(dir, 'secret.json'), '{"secret":true}'); await fs.symlink(path.join(dir, 'secret.json'), path.join(root, 'escape.json'));
  for (const url of ['zah://app/../secret.json', 'zah://app/%2e%2e/secret.json', 'zah://app/a%2f..%2fsecret.json', 'zah://app/escape.json', 'zah://app/index.exe', 'https://app/index.html', 'zah://app/a\\b.js', 'zah://app/%00.js']) await assert.rejects(resolveAsset(root, url));
  const result = await staticResponse(root, APP_URL); assert.equal(result.status, 200); assert.equal(await result.text(), '<h1>ZaH</h1>');
  assert.ok(result.headers.get('content-security-policy')?.includes("connect-src 'none'")); assert.equal(result.headers.get('x-content-type-options'), 'nosniff');
  assert.equal((await staticResponse(root, APP_URL, 'POST')).status, 405);
});
test('manager readiness is explicit, bounded and protocol checked', async () => {
  const good = new PassThrough(); const promise = waitForReady(good, '/tmp/zah/socket', 1000);
  good.write('{"ready":true,"protocol":1,'); good.write('"socket":"/tmp/zah/socket"}\n'); await promise;
  const bad = new PassThrough(); const rejected = waitForReady(bad, '/tmp/zah/socket', 1000); bad.end('{"ready":true,"protocol":2,"socket":"/tmp/zah/socket"}\n'); await assert.rejects(rejected);
  const large = new PassThrough(); const oversized = waitForReady(large, '/tmp/zah/socket', 1000); large.write('x'.repeat(4097)); await assert.rejects(oversized);
  const stalled = new PassThrough(); await assert.rejects(waitForReady(stalled, '/tmp/zah/socket', 10), /timed out/);
});
test('child environment keeps existing proxies but strips Electron injection', () => {
  const source = {HTTP_PROXY: 'http://existing:1234', HTTPS_PROXY: 'http://existing:1234', PATH: '/usr/bin', LD_PRELOAD: 'bad.so', NODE_OPTIONS: '--inspect', ELECTRON_RUN_AS_NODE: '1', DYLD_LIBRARY_PATH: 'private'};
  const clean = childEnvironment(source); assert.equal(clean.HTTP_PROXY, source.HTTP_PROXY); assert.equal(clean.HTTPS_PROXY, source.HTTPS_PROXY);
  assert.equal(clean.LD_PRELOAD, undefined); assert.equal(clean.NODE_OPTIONS, undefined); assert.equal(clean.ELECTRON_RUN_AS_NODE, undefined); assert.equal(clean.DYLD_LIBRARY_PATH, undefined);
  assert.equal(source.LD_PRELOAD, 'bad.so');
});
test('socket endpoint rejects regular file even in private directory', async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'zah-endpoint-')); t.after(() => fs.rm(dir, {recursive: true, force: true}));
  const file = path.join(dir, 'manager.sock'); await fs.writeFile(file, 'keep', {mode: 0o600});
  await assert.rejects(validateEndpoint(file)); assert.equal(await fs.readFile(file, 'utf8'), 'keep');
});

test('Windows registration and full environment backups require separate path grants', () => {
  const grants = new PathGrants(); grants.add('/tmp/bottle', 'directory');
  const registration = {operation: 'instance.register', input: {id: 'example', bottle_path: '/tmp/bottle', executable_path: '/tmp/bottle/app.exe', program_name: 'App'}};
  assert.throws(() => grants.validate('plan.create', registration));
  grants.add('/tmp/bottle/app.exe', 'file'); assert.doesNotThrow(() => grants.validate('plan.create', registration));
  grants.add('/tmp/full.zahbundle', 'save');
  const backup = {operation: 'environment.export', input: {id: 'example', path: '/tmp/full.zahbundle', cold_confirmed: true, bottles_archive_path: '/tmp/manual.tar.gz'}};
  assert.throws(() => grants.validate('plan.create', backup)); grants.add('/tmp/manual.tar.gz', 'file'); assert.doesNotThrow(() => grants.validate('plan.create', backup));
  assert.throws(() => grants.validate('plan.create', {operation: 'environment.restore', input: {path: '/tmp/full.zahbundle'}}));
  grants.add('/tmp/full.zahbundle', 'file'); assert.doesNotThrow(() => grants.validate('plan.create', {operation: 'environment.restore', input: {path: '/tmp/full.zahbundle'}}));
});

// Cross-language contract guard: new Go methods/operations require conscious IPC review.
test('desktop allowlists track the implemented Go service contract', async () => {
  const source = await fs.readFile(path.resolve('internal/service/service.go'), 'utf8');
  const handle = source.slice(source.indexOf('func (s *Service) Handle('), source.indexOf('type planRequest struct'));
  const plan = source.slice(source.indexOf('func (s *Service) plan('), source.indexOf('// Execute'));
  const names = (text: string) => new Set(Array.from(text.matchAll(/^\tcase (.+):$/gm)).flatMap(match => Array.from(match[1]!.matchAll(/"([a-z]+(?:\.[A-Za-z]+)+)"/g)).map(m => m[1]!)));
  const publicMethods=names(handle);assert.equal(publicMethods.delete("installation.location"),true);assert.throws(()=>validateCall("installation.location",{id:"a".repeat(32)}));
  assert.deepEqual([...METHODS].sort(), [...publicMethods, "manager.retry", "host.dependencies.confirm"].sort());
  assert.deepEqual([...PLAN_OPERATIONS].sort(), [...names(plan)].sort());
});


test('whole-workbench export validates every nested file grant and input shape', () => {
  const grants = new PathGrants();
  grants.add('/tmp/workbench.zahbundle', 'save'); grants.add('/tmp/first.tar.gz', 'file');
  const first = {id: 'first', bottles_archive_path: '/tmp/first.tar.gz', bottles_archive_sha256: 'a'.repeat(64)};
  const second = {id: 'second', bottles_archive_path: '/tmp/second.tar.gz'};
  const input = {path: '/tmp/workbench.zahbundle', cold_confirmed: true, instances: [first, second]};
  const validate = (value: unknown) => grants.validate('plan.create', {operation: 'workbench.export', input: value});
  assert.throws(() => validate(input), /system dialog/);
  grants.add('/tmp/second.tar.gz', 'file'); assert.doesNotThrow(() => validate(input));
  assert.doesNotThrow(() => validate({...input, instances: [{id: 'linux-instance'}]}));
  assert.doesNotThrow(() => validate({...input, include_catalog: true}));
  assert.doesNotThrow(() => validate({...input, include_catalog: false}));
  for (const bad of [
    {...input, path: '/tmp/unselected.zahbundle'}, {...input, cold_confirmed: false}, {...input, cold_confirmed: 'true'},
    {...input, include_catalog: 'true'}, {...input, include_catalog: null}, {...input, hidden_path: '/etc/passwd'}, {...input, instances: []}, {...input, instances: {}},
    {...input, instances: [null]}, {...input, instances: [[]]}, {...input, instances: ['first']},
    {...input, instances: [first, first]}, {...input, instances: [{id: '../escape'}]},
    {...input, instances: [{id: 'first', extra: true}]}, {...input, instances: [{...first, bottles_archive_path: '/etc/passwd'}]},
    {...input, instances: [{...first, bottles_archive_path: null}]}, {...input, instances: [{...first, bottles_archive_sha256: 'wrong'}]},
    {...input, instances: [{id: 'first', bottles_archive_sha256: 'a'.repeat(64)}]},
    {...input, instances: Array.from({length: 1001}, (_, i) => ({id: 'instance-' + i}))}
  ]) assert.throws(() => validate(bad));
  assert.doesNotThrow(() => validate({...input, instances: Array.from({length: 1000}, (_, i) => ({id: 'instance-' + i}))}));
  grants.clear(); assert.throws(() => validate(input));
});


test('cleanup plans require stable IDs, exact fields and explicit confirmations', () => {
  const grants = new PathGrants(); const id = 'a'.repeat(32), jobID = 'b'.repeat(32);
  const validate = (operation: string, input: unknown) => grants.validate('plan.create', {operation, input});
  assert.doesNotThrow(() => validate('instance.forget', {id}));
  assert.doesNotThrow(() => validate('instance.data.remove', {id, cold_confirmed: true, confirm_private_data: true}));
  assert.doesNotThrow(() => validate('runtime.remove', {job_id: jobID, instance_id: id, external_quiet_confirmed: true}));
  for (const bad of [undefined, '', '../escape', id.toUpperCase(), 'x'.repeat(32), id + 'a', 123]) {
    assert.throws(() => validate('instance.forget', {id: bad}));
    assert.throws(() => validate('runtime.remove', {job_id: bad, instance_id: id, external_quiet_confirmed: true}));
    assert.throws(() => validate('runtime.remove', {job_id: jobID, instance_id: bad, external_quiet_confirmed: true}));
  }
  for (const confirmation of [undefined, false, 'true', 1, null]) {
    assert.throws(() => validate('runtime.remove', {job_id: jobID, instance_id: id, external_quiet_confirmed: confirmation}));
    assert.throws(() => validate('instance.data.remove', {id, cold_confirmed: confirmation, confirm_private_data: true}));
    assert.throws(() => validate('instance.data.remove', {id, cold_confirmed: true, confirm_private_data: confirmation}));
  }
  assert.throws(() => validate('instance.forget', {id, path: '/tmp/user-files'}));
  assert.throws(() => validate('instance.data.remove', {id, cold_confirmed: true, confirm_private_data: true, recursive: true}));
  assert.throws(() => validate('runtime.remove', {job_id: jobID, instance_id: id, external_quiet_confirmed: true, image_id: 'arbitrary-image'}));
});


test('age encryption and decryption accept only independently selected files and public recipients', () => {
  const grants = new PathGrants();
  grants.add('/tmp/source.zahbundle', 'file'); grants.add('/tmp/cipher.zahbundle.age', 'save');
  const key = 'age1' + 'q'.repeat(58);
  const encrypt = {path: '/tmp/source.zahbundle', destination: '/tmp/cipher.zahbundle.age', recipients: [key]};
  const validate = (operation: string, input: unknown) => grants.validate('plan.create', {operation, input});
  assert.doesNotThrow(() => validate('bundle.encrypt', encrypt));
  for (const recipients of [[], key, [key, key], ['AGE-SECRET-KEY-1private'], ['ssh-rsa any'], ['age1short'], ['age1' + 'q'.repeat(253)], [null], Array.from({length: 129}, (_, i) => key + i)])
    assert.throws(() => validate('bundle.encrypt', {...encrypt, recipients}));
  assert.throws(() => validate('bundle.encrypt', {...encrypt, path: '/etc/passwd'}));
  assert.throws(() => validate('bundle.encrypt', {...encrypt, identity_content: 'private'}));
  const decrypt = {path: '/tmp/cipher.zahbundle.age', destination: '/tmp/restored.zahbundle', identity_path: '/tmp/identity.txt'};
  grants.add('/tmp/cipher.zahbundle.age', 'file'); grants.add('/tmp/restored.zahbundle', 'save');
  assert.throws(() => validate('bundle.decrypt', decrypt));
  grants.add('/tmp/identity.txt', 'file'); assert.doesNotThrow(() => validate('bundle.decrypt', decrypt));
  assert.throws(() => validate('bundle.decrypt', {...decrypt, identity_path: 'AGE-SECRET-KEY-1private'}));
  assert.throws(() => validate('bundle.decrypt', {...decrypt, private_key: 'private'}));
  assert.throws(() => validate('bundle.decrypt', {...decrypt, destination: '/tmp/cipher.zahbundle.age'}));
});

test('shared-content export accepts only bounded unique registered binding IDs', () => {
  const grants = new PathGrants(); grants.add('/tmp/export.zahbundle', 'save');
  const valid = Array.from({length: 128}, (_, i) => i.toString(16).padStart(32, '0'));
  const check = (ids: unknown) => {
    grants.validate('plan.create', {operation: 'environment.export', input: {id: 'a'.repeat(32), path: '/tmp/export.zahbundle', cold_confirmed: true, include_bindings: ids}});
    grants.validate('plan.create', {operation: 'workbench.export', input: {path: '/tmp/export.zahbundle', cold_confirmed: true, instances: [{id: 'a'.repeat(32), include_bindings: ids}]}});
  };
  assert.doesNotThrow(() => check(undefined)); assert.doesNotThrow(() => check([])); assert.doesNotThrow(() => check(valid));
  for (const invalid of [null, {}, 'binding', ['/home/user'], [valid[0], valid[0]], [1], ['A'.repeat(32)], [...valid, 'b'.repeat(32)]]) assert.throws(() => check(invalid));
});

test('save format has only plain and encrypted modes without extra bridge methods', () => {
  assert.equal(bundleSuffix(), '.zahbundle'); assert.equal(bundleSuffix(undefined), '.zahbundle');
  assert.equal(bundleSuffix('plain'), '.zahbundle'); assert.equal(bundleSuffix('encrypted'), '.zahbundle.age');
  for (const value of ['../escape', 'age', null, {}, true, 1]) assert.throws(() => bundleSuffix(value));
});


test('discarding an unsubmitted plan exposes only one stable plan ID', () => {
  const grants = new PathGrants(), id = 'a'.repeat(32);
  assert.doesNotThrow(() => {validateCall('plan.discard', {id});grants.validate('plan.discard', {id});});
  for (const value of [{}, {id: ''}, {id: '../escape'}, {id: 'f'.repeat(64)}, {id, force: true}, {id, path: '/tmp/file'}, {plan_id: id}])
    assert.throws(() => grants.validate('plan.discard', value));
});

test('event resume accepts bounded safe cursors and rejects ambiguous or excessive reads', () => {
  const grants = new PathGrants();
  for (const params of [{}, {after: null}, {after: 0}, {after: Number.MAX_SAFE_INTEGER}, {limit: 1}, {after: 42, limit: 500}])
    assert.doesNotThrow(() => {validateCall('events.resume', params);grants.validate('events.resume', params);});
  for (const after of [-1, 1.5, Number.MAX_SAFE_INTEGER + 1, NaN, Infinity, '42', {}, []])
    assert.throws(() => grants.validate('events.resume', {after}));
  for (const limit of [0, -1, 501, 1.5, null, '100', NaN]) assert.throws(() => grants.validate('events.resume', {limit}));
  assert.throws(() => grants.validate('events.resume', {after: 0, snapshot: true}));
});


test('stopping registered applications requires exact explicit confirmation', () => {
  const grants = new PathGrants();
  const validate = (input: unknown, extra: Record<string, unknown> = {}) => {
    const params = {operation: 'applications.stopAll', input, ...extra};
    validateCall('plan.create', params); grants.validate('plan.create', params);
  };
  assert.doesNotThrow(() => validate({confirmed: true}));
  for (const input of [undefined, null, [], {}, {confirmed: false}, {confirmed: 'true'}, {confirmed: 1},
    {confirmed: true, pid: 1234}, {confirmed: true, container: 'foreign'}, {confirmed: true, force: true},
    {confirmed: true, path: '/tmp/unselected'}]) assert.throws(() => validate(input));
  assert.throws(() => validate({confirmed: true}, {force: true}));
});

test('single stop accepts only a persisted instance and icon paths require a native grant',()=>{
 const grants=new PathGrants();const id='a'.repeat(32);
 grants.validate('plan.create',{operation:'instance.stop',input:{id}});
 assert.throws(()=>grants.validate('plan.create',{operation:'instance.stop',input:{id,identity:'foreign'}}));
 assert.throws(()=>grants.validate('release.icon.set',{id,path:'/tmp/icon.png'}));
 grants.add('/tmp/icon.png','file');grants.validate('release.icon.set',{id,path:'/tmp/icon.png'});
});

test('explicit manager retry is local and accepts no data or command', () => { const grants=new PathGrants();assert.doesNotThrow(()=>grants.validate('manager.retry',{}));assert.throws(()=>grants.validate('manager.retry',{command:'anything'})); });

test('installation guide accepts archived identities and entry selection requires a granted managed/file path',()=>{
 const grants=new PathGrants(),id='a'.repeat(32),artifact='b'.repeat(64);
 grants.validate('installation.inspect',{artifact_id:artifact});assert.throws(()=>grants.validate('installation.inspect',{path:'/etc/passwd'}));
 grants.validate('installation.configure',{request_id:id,artifact_id:artifact,name:'App',version:'1',base:'selected',entry:'entry',mode:'portable'});
 assert.throws(()=>grants.validate('installation.configure',{request_id:id,artifact_id:artifact,name:'App',version:'1',base:'selected',entry:'entry',mode:'portable',shell:'bad'}));
 const params={operation:'installation.finish',input:{id,executable_path:'/tmp/bottle/app.exe',confirmed:true}};
 assert.throws(()=>grants.validate('plan.create',params));grants.add('/tmp/bottle/app.exe','file');grants.validate('plan.create',params);
 assert.throws(()=>grants.validate('plan.create',{operation:'installation.maintenance',input:{id,confirmed:true,executable_path:'/etc/passwd'}}));
});

test('renderer cannot forge approval or arbitrary dependency commands',()=>{const grants=new PathGrants();assert.throws(()=>grants.validate('job.submit',{plan_id:'a'.repeat(32),idempotency_key:'test',host_changes_confirmed:true}));assert.throws(()=>grants.validate('host.dependencies.confirm',{plan_id:'a'.repeat(32),idempotency_key:'test',command:'sh'}));assert.doesNotThrow(()=>grants.validate('host.dependencies.confirm',{plan_id:'a'.repeat(32),idempotency_key:'test'}));});

test('management environment deletion requires HOME precheck before a plan can reach Go', () => {
  const grants=new PathGrants();const id='a'.repeat(32);
  assert.doesNotThrow(()=>grants.validate('plan.create',{operation:'instance.remove',input:{id,precheck_private_data:true}}));
  for(const input of [{id},{id,precheck_private_data:false},{id,precheck_private_data:true,path:'/tmp/arbitrary'}])
    assert.throws(()=>grants.validate('plan.create',{operation:'instance.remove',input}),/private-data precheck/);
});
