// Isolated, headless real Electron/preload/React/Go smoke test. Never disables sandbox.
const fs = require('node:fs/promises');
const { watch, existsSync } = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { spawn, execFile } = require('node:child_process');
const { promisify } = require('node:util');
const zlib = require('node:zlib');
const execFileAsync = promisify(execFile);
function previewPNG(r,g,b) {
 const crc = data => {let value=0xffffffff;for(const byte of data){value^=byte;for(let n=0;n<8;n++)value=(value>>>1)^((value&1)?0xedb88320:0)}return (value^0xffffffff)>>>0};
 const chunk=(name,data)=>{const type=Buffer.from(name),size=Buffer.alloc(4),sum=Buffer.alloc(4);size.writeUInt32BE(data.length);sum.writeUInt32BE(crc(Buffer.concat([type,data])));return Buffer.concat([size,type,data,sum])};
 const header=Buffer.alloc(13);header.writeUInt32BE(32,0);header.writeUInt32BE(32,4);header[8]=8;header[9]=6;const pixels=[];for(let y=0;y<32;y++){pixels.push(0);for(let x=0;x<32;x++)pixels.push(r,g,b,(x>4&&x<27&&y>4&&y<27)?255:0)};return Buffer.concat([Buffer.from('89504e470d0a1a0a','hex'),chunk('IHDR',header),chunk('IDAT',zlib.deflateSync(Buffer.from(pixels))),chunk('IEND',Buffer.alloc(0))]);
}
const assert = require('node:assert/strict');

async function main() {
  const binary = path.resolve('node_modules/electron/dist/electron');
  if (!existsSync(binary)) throw new Error('Electron runtime missing. Run npm run setup:electron first.');
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'zah-desktop-test-'));
  const env = {...process.env};
  // Exercise the actual first-start default XDG layout, including the 0775
  // shared state parent that caused startup to fail on the user's machine.
  for (const key of ['XDG_DATA_HOME','XDG_CONFIG_HOME','XDG_STATE_HOME','XDG_CACHE_HOME']) delete env[key];
  env.HOME = path.join(root, 'home'); await fs.mkdir(env.HOME, {mode: 0o700});
  const stateParent=path.join(env.HOME,'.local','state');await fs.mkdir(stateParent,{recursive:true,mode:0o700});await fs.chmod(stateParent,0o775);
  env.XDG_RUNTIME_DIR=path.join(root,'run');await fs.mkdir(env.XDG_RUNTIME_DIR,{mode:0o700});
  // Installation-guide integration uses a deliberately narrow fake image
  // inventory. All other Docker operations fail; no host daemon is contacted.
  const mockBin=path.join(root,'mock-bin');await fs.mkdir(mockBin,{mode:0o700});
  await fs.writeFile(path.join(mockBin,'docker'),'#!/usr/bin/env node\n'+
    'if(JSON.stringify(process.argv.slice(2))!==JSON.stringify(["image","ls","--digests","--no-trunc","--format","{{json .}}"])){console.error("unexpected mock Docker operation");process.exit(2)}\n'+
    'console.log(JSON.stringify({Repository:"ubuntu",Tag:"22.04",Digest:"sha256:'+ 'a'.repeat(64)+'"}));\n',{mode:0o700});
  env.PATH=mockBin+path.delimiter+env.PATH;
  delete env.ELECTRON_RUN_AS_NODE; delete env.NODE_OPTIONS;
  // A headless Ozone platform has no connection to the user's X11/Wayland display.
  delete env.DISPLAY; delete env.WAYLAND_DISPLAY;
  const child = spawn(binary, ['.', '--headless', '--ozone-platform=headless', '--disable-gpu', '--remote-debugging-pipe', '--no-first-run', '--disable-background-networking'], {
    cwd: process.cwd(), env, stdio: ['ignore', 'ignore', 'pipe', 'pipe', 'pipe']
  });
  let stderr = ''; child.stderr.on('data', chunk => {stderr = (stderr + chunk.toString()).slice(-64 * 1024);});
  const exited = new Promise(resolve => {child.once('exit', (code, signal) => resolve({code, signal}));child.once('error', error => resolve({error}));});
  let sequence = 0, buffer = Buffer.alloc(0); const pending = new Map(); const events = new Map();
  const fail = error => {for (const item of pending.values()) {clearTimeout(item.timer); item.reject(error);} pending.clear(); for (const item of events.values()) {clearTimeout(item.timer); item.reject(error);} events.clear();};
  child.stdio[3].on('error', error => fail(error)); child.stdio[4].on('error', error => fail(error));
  child.once('error', error => fail(error)); child.once('exit', () => fail(new Error('Headless Electron exited before smoke test completed: ' + stderr)));
  child.stdio[4].on('data', chunk => {buffer = Buffer.concat([buffer, chunk]); let end; while ((end = buffer.indexOf(0)) >= 0) {
    const raw = buffer.subarray(0,end); buffer = buffer.subarray(end+1); if (!raw.length) continue;
    let message; try {message = JSON.parse(raw.toString());} catch {fail(new Error('Invalid CDP response'));return;}
    if (message.id) {const item = pending.get(message.id);if(!item)continue;pending.delete(message.id);clearTimeout(item.timer);message.error?item.reject(new Error(message.error.message)):item.resolve(message.result);}
    else if (message.method) {const item = events.get(message.method);if(item && (!item.predicate || item.predicate(message.params))) {events.delete(message.method);clearTimeout(item.timer);item.resolve(message.params);}}
  }});
  const send = (method,params={},sessionId) => new Promise((resolve,reject)=>{const id=++sequence;const timer=setTimeout(()=>{pending.delete(id);reject(new Error('CDP timeout: '+method+'\n'+stderr));},20000);pending.set(id,{resolve,reject,timer});child.stdio[3].write(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})})+'\0', error=>{if(error){const item=pending.get(id);if(item){pending.delete(id);clearTimeout(item.timer);item.reject(error);}}});});
  const event = (method,predicate) => new Promise((resolve,reject)=>{const timer=setTimeout(()=>{events.delete(method);reject(new Error('CDP event timeout: '+method+'\n'+stderr));},20000);events.set(method,{resolve,reject,timer,predicate});});
  let completed = false;
  try {
    const appeared = event('Target.targetCreated', p => p.targetInfo.type === 'page'); appeared.catch(()=>undefined);
    await send('Target.setDiscoverTargets',{discover:true});
    const targets = await send('Target.getTargets');
    let target = targets.targetInfos.find(t=>t.type==='page');
    if (!target) target = (await appeared).targetInfo; else {const wait=events.get('Target.targetCreated');if(wait){clearTimeout(wait.timer);events.delete('Target.targetCreated');wait.resolve({targetInfo:target});}}
    const {sessionId} = await send('Target.attachToTarget',{targetId:target.targetId,flatten:true});
    const evaluate = async expression => {const result=await send('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true},sessionId);if(result.exceptionDetails)throw new Error(result.exceptionDetails.text+': '+JSON.stringify(result.exceptionDetails.exception));return result.result.value;};
    await send('Runtime.enable',{},sessionId);
    let documentReady = false;
    for (let attempt = 0; attempt < 3 && !documentReady; attempt++) {
      const changed = event('Runtime.executionContextCreated', p => p.context.auxData?.isDefault === true); changed.catch(()=>undefined);
      try {
        await evaluate(`new Promise((resolve,reject)=>{const check=()=>{if(window.zah && document.querySelector('h1')){clearInterval(timer);clearTimeout(limit);resolve(true)}};const timer=setInterval(check,50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Application document did not become ready'))},10000);check()})`);
        documentReady = true;
        const waiter=events.get('Runtime.executionContextCreated');if(waiter){clearTimeout(waiter.timer);events.delete('Runtime.executionContextCreated');waiter.resolve({});}
      } catch(error) {
        if (!String(error.message).includes('Execution context was destroyed')) throw error;
        await changed;
      }
    }
    assert.ok(documentReady,'Application document must finish its initial navigation');
    const policy = await evaluate(`({require:typeof window.require,process:typeof window.process,keys:Object.keys(window.zah).sort(),title:document.querySelector('h1').textContent})`);
    assert.equal(policy.require,'undefined');assert.equal(policy.process,'undefined');assert.deepEqual(policy.keys,['acceptPackage','call','chooseDirectory','chooseFile','saveBundle']);assert.equal(policy.title,'应用库');
    const info = await evaluate(`window.zah.call('system.info',{})`); assert.equal(info.protocol,1);assert.equal(info.paths,undefined);assert.equal(info.Paths,undefined);
    assert.equal((await fs.stat(stateParent)).mode&0o777,0o775);assert.equal(existsSync(path.join(stateParent,'zah')),false);
    assert.equal((await fs.stat(path.join(env.HOME,'.local','share','zah','state','zah'))).mode&0o777,0o700);
    const rejected = await evaluate(`Promise.all([window.zah.call('shell',{}),window.zah.call('plan.create',{operation:'artifact.import',input:{path:'/etc/passwd'}})].map(p=>p.then(()=>false,()=>true)))`);assert.deepEqual(rejected,[true,true]);
    const setupCode=await evaluate(`window.zah.call('installation.configure',{request_id:'${'a'.repeat(32)}',artifact_id:'${'b'.repeat(64)}',name:'拒绝夹具',version:'1',base:'ubuntu@sha256:${'c'.repeat(64)}',entry:'squashfs-root/AppRun',mode:'native'}).then(()=>null,error=>error.rpcCode)`);
    assert.equal(setupCode,-32014,'definitive pre-save rejection survives Go, main IPC and sandboxed preload');
    assert.deepEqual(await evaluate(`window.zah.call('catalog.list',{kind:'instance'})`),[]);
    const baseline = await evaluate(`window.zah.call('events.resume',{})`);assert.ok(baseline.snapshot);assert.deepEqual(baseline.events,[]);assert.ok(Number.isSafeInteger(baseline.next_cursor));
    const family = await evaluate(`window.zah.call('family.create',{name:'隔离桌面联调'})`);assert.ok(family.id);
    const page = await evaluate(`window.zah.call('events.resume',{after:${baseline.next_cursor},limit:100})`);assert.ok(page.events.some(item=>item.type==='record.changed' && item.data.kind==='family' && item.data.id===family.id));assert.ok(page.next_cursor>baseline.next_cursor);assert.equal(page.snapshot,undefined);
    const recovered = await evaluate(`window.zah.call('events.resume',{after:Number.MAX_SAFE_INTEGER})`);assert.ok(recovered.snapshot.family.some(item=>item.id===family.id));assert.ok(recovered.next_cursor>=page.next_cursor);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.body.innerText.includes('隔离桌面联调')){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Created family did not render'))},10000)})`);
    const library = await evaluate(`({cards:document.querySelectorAll('.application-card').length,versionDisabled:document.querySelector('.version-select select')?.disabled,runDisabled:document.querySelector('.run-button')?.disabled,stopButtons:Array.from(document.querySelectorAll('button')).filter(b=>b.textContent.trim()==='全部停止').length})`);
    assert.equal(library.cards,1);assert.equal(library.versionDisabled,true);assert.equal(library.runDisabled,true);assert.equal(library.stopButtons,1);
    // Exercise cancellation only: never submit a real application stop in smoke.
    await evaluate(`Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()==='全部停止').click()`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.querySelector('dialog[open]')){clearInterval(timer);clearTimeout(limit);resolve(true)}},25);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Stop confirmation did not appear'))},3000)})`);
    await evaluate(`Array.from(document.querySelector('dialog[open]').querySelectorAll('button')).find(b=>b.textContent.trim()==='取消').click()`);
    const afterCancel=await evaluate(`window.zah.call('catalog.list',{kind:'job'})`);assert.deepEqual(afterCancel,[]);
    // Populate actual version and icon records through the real private manager.
    // This fixture material is inert. No instance is installed or launched.
    const rpc=async(method,params)=>JSON.parse((await execFileAsync(path.resolve('bin/zah'),['rpc',method,JSON.stringify(params)],{env,timeout:15000})).stdout);
    const material=path.join(root,'inert-material.AppImage');
    // Static-header fixture only: never execute it or submit its installation.
    const fixture=Buffer.alloc(128);fixture.write('\x7fELF',0,'binary');fixture[4]=2;fixture[5]=1;fixture.write('AI\x02',8,'binary');fixture.writeUInt16LE(62,18);await fs.writeFile(material,fixture);
    const importPlan=await rpc('plan.create',{operation:'artifact.import',input:{path:material}});
    const importJob=await rpc('job.submit',{plan_id:importPlan.id,idempotency_key:'smoke-icon-material'});
    let imported;
    for(let i=0;i<100;i++){imported=(await rpc('catalog.get',{kind:'job',id:importJob.id})).data;if(['succeeded','failed','interrupted'].includes(imported.state))break;await new Promise(resolve=>setTimeout(resolve,50))}
    assert.equal(imported.state,'succeeded');const artifacts=await rpc('catalog.list',{kind:'artifact'});
    const versions=[];
    for(const [version,color] of [['1.0',[110,84,218]],['2.0',[30,159,142]]]){
      const release=await rpc('release.create',{family_id:family.id,platform:'linux',arch:'amd64',version,artifacts:[artifacts[0].id]});
      const iconPath=path.join(root,'icon-'+version+'.png');await fs.writeFile(iconPath,previewPNG(...color));
      await rpc('release.icon.set',{id:release.id,path:iconPath});versions.push(release);
    }
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.querySelector('.version-select select')?.options.length===2&&document.querySelector('.icon-picker img')?.complete){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Version icons did not render'))},10000)})`);
    const firstIcon=await evaluate(`document.querySelector('.icon-picker img').src`);
    await evaluate(`(()=>{const s=document.querySelector('.version-select select');s.value=${JSON.stringify(versions[1].id)};s.dispatchEvent(new Event('change',{bubbles:true}))})()`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{const img=document.querySelector('.icon-picker img');if(img?.alt.includes('2.0')&&img.complete&&img.naturalWidth>0){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Selected version icon did not load'))},5000)})`);
    const secondIcon=await evaluate(`document.querySelector('.icon-picker img').src`);assert.notEqual(firstIcon,secondIcon);assert.equal(await evaluate(`document.querySelector('.version-select select').disabled`),false);
    assert.equal(await evaluate(`document.querySelector('.run-button').disabled`),true);
    await send('Emulation.setDeviceMetricsOverride',{width:1240,height:840,deviceScaleFactor:1,mobile:false},sessionId);
    const screenshot = await send('Page.captureScreenshot',{format:'png'},sessionId);
    const screenshotPath=path.resolve('test-results/desktop-smoke.png');await fs.mkdir(path.dirname(screenshotPath),{recursive:true});await fs.writeFile(screenshotPath,Buffer.from(screenshot.data,'base64'));
    // The simplified app never exposes archive/recipe/task pages. Exercise the
    // actual configuration API with an inert fixture, then verify that missing
    // mocked prerequisites are visible instead of executing a host container.
    const guided=await rpc('installation.configure',{request_id:require('node:crypto').randomBytes(16).toString('hex'),artifact_id:artifacts[0].id,family_id:family.id,name:family.name,version:'3.0',base:'ubuntu@sha256:'+ 'a'.repeat(64),entry:'squashfs-root/AppRun',mode:'native'});
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{const s=document.querySelector('.version-select select');if(s?.options.length===3){clearInterval(timer);clearTimeout(limit);resolve(true)}},25);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Configured version did not render'))},10000)})`);
    await evaluate(`(()=>{const s=document.querySelector('.version-select select');s.value=${JSON.stringify(guided.release_id)};s.dispatchEvent(new Event('change',{bubbles:true}))})()`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{const b=document.querySelector('.run-button');if(b?.textContent.includes('继续安装')){clearInterval(timer);clearTimeout(limit);b.click();resolve(true)}},25);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Continue installation missing'))},5000)})`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.querySelector('[role=alert]')){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Missing prerequisite was not reported'))},15000)})`);
    const installationJobs=await rpc('catalog.list',{kind:'job'});assert.ok(installationJobs.length>1);
    for(const job of installationJobs)if(job.data.state==='waiting_user')await rpc('job.cancel',{id:job.id});
    assert.equal((await rpc('catalog.get',{kind:'instance',id:guided.id})).data.state,'unprepared');
    console.log('PASS: real setup records → card continue installation → mocked prerequisite failure shown; no host container contacted');
    await evaluate(`Array.from(document.querySelectorAll('button')).find(b=>b.textContent==='关闭提示')?.click()`);
    await evaluate(`window.location.hash = '#/manage'`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.querySelector('h1')?.textContent==='管理'&&document.body.textContent.includes('检查完成')){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Management dependency check missing'))},10000)})`);
    assert.deepEqual(await evaluate(`Array.from(document.querySelectorAll('nav a')).map(a=>a.textContent.trim())`),['应用库','管理','迁移']);
    const managementShot=await send('Page.captureScreenshot',{format:'png'},sessionId);await fs.writeFile(path.resolve('test-results/desktop-management-smoke.png'),Buffer.from(managementShot.data,'base64'));
    await evaluate(`window.location.hash = '#/migration'`);
    await evaluate(`new Promise((resolve,reject)=>{const timer=setInterval(()=>{if(document.querySelector('h1')?.textContent.includes('迁移')){clearInterval(timer);clearTimeout(limit);resolve(true)}},50);const limit=setTimeout(()=>{clearInterval(timer);reject(new Error('Migration route did not render'))},10000)})`);
    await send('Emulation.setDeviceMetricsOverride',{width:1240,height:1100,deviceScaleFactor:1,mobile:false},sessionId);
    const migrationShot=await send('Page.captureScreenshot',{format:'png'},sessionId);
    const migrationPath=path.resolve('test-results/desktop-migration-smoke.png');await fs.writeFile(migrationPath,Buffer.from(migrationShot.data,'base64'));
    completed = true; console.log('PASS: headless Electron → sandboxed preload → React → private Go manager → isolated SQLite; incremental events and snapshot recovery');console.log('Screenshot:',screenshotPath);console.log('Migration screenshot:',migrationPath);
    // Do not invoke Browser.close: production close requires user confirmation.
    // The finally block terminates only this isolated, test-owned child.
  } catch (error) {
    console.error('Smoke operation failed:', error instanceof Error ? error.message : error);
    throw error;
  } finally {
    if (!completed && stderr) console.error(stderr);
    // SIGTERM affects only the test-owned Electron child, never user sessions.
    if (child.exitCode===null && child.signalCode===null) child.kill('SIGTERM');
    let exitTimer;
    const stopped = await Promise.race([exited.then(()=>true),new Promise(resolve=>{exitTimer=setTimeout(()=>resolve(false),3000)})]);
    clearTimeout(exitTimer);
    if (!stopped) { child.kill('SIGKILL'); await exited; }
    fail(new Error('Smoke test complete'));
    const socket=path.join(env.XDG_RUNTIME_DIR,'zah','manager.sock');
    let active=true;try{await fs.lstat(socket)}catch{active=false;}
    if(active) await new Promise((resolve,reject)=>{const timer=setTimeout(()=>{watcher.close();reject(new Error('Isolated manager still alive; retained '+root))},40000);const check=async()=>{try{await fs.lstat(socket)}catch{clearTimeout(timer);watcher.close();resolve()}};const watcher=watch(path.dirname(socket),check);void check();});
    try { await fs.rm(root,{recursive:true,force:true,maxRetries:5,retryDelay:100}); }
    catch (error) { console.error('Isolated test cleanup incomplete; retained:',root,error.message); }

  }
}
main().catch(error=>{console.error(error.message);process.exitCode=1;});
