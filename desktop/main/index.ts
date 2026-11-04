import {isSavedEntryRegistration} from './saved-entry';
import { app, BrowserWindow, dialog, ipcMain, Menu, protocol, session, type IpcMainInvokeEvent } from 'electron';
import fs from 'node:fs/promises';
import path from 'node:path';
import { ManagerConnection } from './manager';
import { ManagerRPCError } from './rpc-client';
import { APP_URL, isAppDocument, PathGrants, validateCall, bundleSuffix } from './security';
import { staticResponse } from '../protocol/static';
import {DependencyConfirmation} from './dependency-confirmation';
import { CloseCoordinator, CLOSE_READ_METHODS } from './close-coordinator';

// Register before app ready. No external URL handler is installed in the OS.
protocol.registerSchemesAsPrivileged([{scheme: 'zah', privileges: {standard: true, secure: true, supportFetchAPI: true, corsEnabled: true}}]);
app.setName('ZaH');
app.enableSandbox();
let window: BrowserWindow | null = null;
let manager: ManagerConnection | undefined;
const grants = new PathGrants();
let inFlight = 0;
let writesInFlight = 0;
let mutationRevision = 0;
let dialogOpen = false;
const closing = new CloseCoordinator({
  call: (method, params) => { if (!manager) return Promise.reject(new Error('管理服务尚未就绪，窗口保持打开。')); return manager.call(method, params); },
  mutationRevision: () => mutationRevision,
  drainWrites: async () => {
    const deadline = Date.now() + 30_000;
    while (writesInFlight > 0) {
      if (Date.now() >= deadline) throw new Error('已有操作尚未返回，窗口保持打开。请等待任务状态明确后再关闭。');
      await new Promise<void>(resolve => setTimeout(resolve, 50));
    }
  },
  confirm: async () => {
    if (!window || dialogOpen) return false;
    dialogOpen = true;
    try {
      const choice = await dialog.showMessageBox(window, {type: 'warning', title: '停止全部应用并退出 ZaH',
        message: '退出前，是否停止全部由 ZaH 管理的应用？',
        detail: '未保存的数据可能丢失。只有逐项确认停止成功后，ZaH 才会退出；取消则保持窗口和应用运行。',
        buttons: ['取消', '全部停止并退出'], defaultId: 0, cancelId: 0, noLink: true});
      return choice.response === 1;
    } finally { dialogOpen = false; }
  },
  report: async message => {
    if (!window || window.isDestroyed()) return;
    dialogOpen = true;
    try { await dialog.showMessageBox(window, {type: 'error', title: 'ZaH 保持打开', message, buttons: ['返回窗口'], noLink: true}); }
    finally { dialogOpen = false; }
  },
  quit: () => app.quit()
});

function assertSender(event: IpcMainInvokeEvent): BrowserWindow {
  if (!window || event.sender !== window.webContents || event.senderFrame !== window.webContents.mainFrame || !isAppDocument(event.senderFrame.url))
    throw new Error('Untrusted IPC sender');
  return window;
}
function safeMessage(error: unknown): string { return error instanceof Error ? error.message.slice(0, 4096) : 'Local operation failed'; }
async function selectedPath(event: IpcMainInvokeEvent, kind: 'file' | 'directory' | 'save', format: unknown = 'plain', instanceID?:string): Promise<string | null> {
  const owner = assertSender(event);
  if (dialogOpen) throw new Error('A file dialog is already open');
  dialogOpen = true;
  try {
    let selected: string | undefined;
    if (kind === 'save') {
      const suffix = bundleSuffix(format);
      const encrypted = suffix === '.zahbundle.age';
      const result = await dialog.showSaveDialog(owner, {title: encrypted ? '保存加密 ZaH 归档' : '保存 ZaH 归档', defaultPath: 'workbench' + suffix, filters: [{name: encrypted ? 'ZaH age 加密归档' : 'ZaH 归档', extensions: [encrypted ? 'age' : 'zahbundle']}], properties: ['showOverwriteConfirmation']});
      if (result.canceled) return null;
      selected = result.filePath;
      if (selected && !selected.endsWith(suffix)) selected += suffix;
      if (selected) {
        try { await fs.lstat(selected); throw new Error('请使用未占用的归档文件名；ZaH 不覆盖已有备份。'); }
        catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
      }
    } else {
      let location: {path:string}|undefined;
      if(instanceID) location=await manager!.call('installation.location',{id:instanceID}) as {path:string};
      const result = await dialog.showOpenDialog(owner, {title: instanceID?'选择此环境内安装好的 EXE':kind === 'file' ? '选择安装包或 ZaH 迁移包' : '选择目录',...(location?{defaultPath:location.path,filters:[{name:'Windows executable',extensions:['exe','EXE']}]}:{}), properties: kind === 'file' ? ['openFile'] : ['openDirectory']});
      if (result.canceled) return null;
      selected = result.filePaths[0];
    }
    if (!selected) return null;
    grants.add(selected, kind);
    return selected;
  } finally { dialogOpen = false; }
}

const dependencies=new DependencyConfirmation((method,params)=>manager!.call(method,params),async detail=>{
 if(!window||dialogOpen||closing.busy||closing.allowed)throw new Error('请先完成当前对话框后再安装依赖。');
 dialogOpen=true;
 try {const choice=await dialog.showMessageBox(window,{type:'warning',title:'确认准备依赖',message:'确认准备以下依赖；需要系统权限时将另行请求授权。',detail,buttons:['取消','确认安装'],defaultId:0,cancelId:0,noLink:true});return choice.response===1}
 finally {dialogOpen=false}
});
function registerIPC(): void {
  ipcMain.handle('zah:call', async (event, method: unknown, input: unknown) => {
    assertSender(event);
    validateCall(method, input);
    if ((closing.busy || closing.allowed) && !CLOSE_READ_METHODS.has(method as string)) throw new Error('正在确认全部停止；请完成或取消关闭操作后再修改应用。');
    if (inFlight >= 16) throw new Error('Too many pending desktop operations');
    inFlight++;
    const writing = method !== 'manager.retry' && !CLOSE_READ_METHODS.has(method as string);
    if (writing) { writesInFlight++; mutationRevision++; }
    try {
    if(!await isSavedEntryRegistration(method as string,input,(name,params)=>manager!.call(name,params)))grants.validate(method as string, input);
      if(method==='host.dependencies.confirm')return {ok:true,value:await dependencies.run(input.plan_id as string,input.idempotency_key as string)};
      if (method === 'manager.retry') manager!.retryStartup();
      const result = await manager!.call(method === 'manager.retry' ? 'system.info' : method as string, input);
      // Discovery returns only verified EXEs inside this registered Bottle.
      // They are selectable managed resources, never renderer-supplied host paths.
      if(method==='installation.programs' && Array.isArray(result)) for(const item of result) {
        if(item && typeof item==='object' && typeof item.path==='string') grants.add(item.path,'file');
      }
      if ((method === 'system.info' || method === 'manager.retry') && result && typeof result === 'object') {
        const {paths: _paths, Paths: _Paths, ...publicInfo} = result as Record<string, unknown>;
        return {ok: true, value: publicInfo};
      }
      return {ok: true, value: result};
    } catch (error) { return {ok: false, error: safeMessage(error), ...(error instanceof ManagerRPCError?{rpcCode:error.rpcCode}:{})}; }
    finally { inFlight--; if (writing) writesInFlight--; }
  });
  ipcMain.handle('zah:accept-package', async (event, selected: unknown) => {
    assertSender(event);
    if (dialogOpen || closing.busy || closing.allowed) throw new Error('请先完成当前对话框。');
    if (typeof selected !== 'string' || !path.isAbsolute(selected) || selected.length > 4096 || selected.includes('\0')) throw new Error('无效的安装包路径。');
    const info = await fs.lstat(selected);
    if (!info.isFile() || info.isSymbolicLink()) throw new Error('请拖入普通安装包文件。');
    grants.add(selected, 'file');
    return selected;
  });
  ipcMain.handle('zah:choose-file', (event,options:unknown) => {
    if(options===undefined)return selectedPath(event,'file');
    if(!options||typeof options!=='object'||Array.isArray(options)||Object.keys(options).length!==1||!('instanceID' in options)||typeof options.instanceID!=='string'||!/^[a-f0-9]{32}$/.test(options.instanceID))throw new Error('A registered instance ID is required');
    return selectedPath(event,'file','plain',options.instanceID);
  });
  ipcMain.handle('zah:choose-directory', event => selectedPath(event, 'directory'));
  ipcMain.handle('zah:save-bundle', (event, format: unknown) => { bundleSuffix(format); return selectedPath(event, 'save', format); });
}

async function createWindow(): Promise<void> {
  const preload = path.join(__dirname, '../preload/index.js');
  window = new BrowserWindow({width: 1240, height: 840, minWidth: 900, minHeight: 650, backgroundColor: '#f5f5f4', title: 'ZaH',
    webPreferences: {preload, sandbox: true, contextIsolation: true, nodeIntegration: false, webSecurity: true, allowRunningInsecureContent: false, webviewTag: false, spellcheck: false}
  });
  const current = window;
  current.webContents.setWindowOpenHandler(() => ({action: 'deny'}));
  current.webContents.on('will-navigate', (event, url) => { if (!isAppDocument(url)) event.preventDefault(); });
  current.webContents.on('will-attach-webview', event => event.preventDefault());
  current.webContents.on('did-start-navigation', (_event, _url, isInPlace, isMainFrame) => { if (isMainFrame && !isInPlace) grants.clear(); });
  current.on('close', event => { if (!closing.allowed) { event.preventDefault(); void closing.request(); } });
  current.on('closed', () => { if (window === current) window = null; grants.clear(); });
  await current.loadURL(APP_URL);
}

if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on('second-instance', () => { if (window) { if (window.isMinimized()) window.restore(); window.focus(); } });
  app.whenReady().then(async () => {
    if (process.platform !== 'linux' || process.arch !== 'x64' || process.getuid?.() === 0) throw new Error('ZaH requires a normal-user Linux x86_64 desktop.');
    const root = app.getAppPath();
    const binary = app.isPackaged ? path.join(process.resourcesPath, 'zah') : path.join(root, 'bin', 'zah');
    manager = new ManagerConnection(binary);
    const resourceRoot = app.isPackaged ? path.join(root, 'frontend', 'dist') : path.join(root, 'frontend', 'dist');
    session.defaultSession.setPermissionRequestHandler((_webContents, _permission, callback) => callback(false));
    session.defaultSession.setPermissionCheckHandler(() => false);
    session.defaultSession.webRequest.onBeforeRequest((details, callback) => callback({cancel: !details.url.startsWith('zah://app/') && !details.url.startsWith('data:')}));
    protocol.handle('zah', request => staticResponse(resourceRoot, request.url, request.method));
    Menu.setApplicationMenu(Menu.buildFromTemplate([{label: '编辑', submenu: [{role: 'undo'}, {role: 'redo'}, {type: 'separator'}, {role: 'cut'}, {role: 'copy'}, {role: 'paste'}, {role: 'selectAll'}]}]));
    registerIPC();
    // Window loading and manager readiness proceed independently. UI reports a
    // real connection error and can retry; it never shows invented ready state.
    void manager.call('system.info', {}).catch(() => undefined);
    await createWindow();
  }).catch(error => { dialog.showErrorBox('ZaH 启动失败', safeMessage(error)); app.quit(); });
}
app.on('window-all-closed', () => app.quit());
app.on('before-quit', event => {
  if (window && !window.isDestroyed() && !closing.allowed) { event.preventDefault(); void closing.request(); return; }
  manager?.close();
});
