import {beforeEach,describe,it,expect,vi} from 'vitest';
import {render,screen,waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {InstallationGuide,InstallationSession} from './InstallationGuide';
import type {Instance} from '../lib/api';
const calls=vi.fn();
beforeEach(()=>{calls.mockReset();Object.defineProperty(window,'zah',{value:{call:calls,chooseFile:vi.fn(async()=>'/tmp/bottle/drive_c/installed.exe'),chooseDirectory:vi.fn(),saveBundle:vi.fn()},configurable:true});});
function view(node:React.ReactNode){render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}>{node}</QueryClientProvider>);return userEvent.setup()}
const instance:Instance={id:'i',release_id:'r',environment_id:'e',name:'应用 1',state:'ready',favorite:false,recipe_revision:'recipe',original_artifact:'a',observed_installed_version:'',drift:'unknown',installation:{request_sha256:'hash',format:'zip',mode:'installer',source_entry:'setup.exe',stage:'awaiting_completion'}};
describe('guided installation',()=>{
 it('lets users correct a proven rejection with a new identity, but not an uncertain response',async()=>{
  let rejected=true;calls.mockImplementation(async(method:string)=>{if(method==='installation.inspect')return{format:'appimage',platform:'linux',name:'App',version:'1',entries:[{name:'App',path:'squashfs-root/AppRun'}],notes:[]};if(method==='installation.options')return[{label:'Ubuntu',value:'fixed'}];if(rejected)throw Object.assign(new Error('duplicate version'),{rpcCode:-32014});return{id:'saved'}});
  const onReady=vi.fn(async()=>{});const user=view(<InstallationGuide artifactID={'a'.repeat(64)} family={{id:"f",name:"App",platform:"linux"}} onClose={()=>{}} onReady={onReady}/>);
  await waitFor(()=>expect(screen.getByRole('button',{name:'开始安装'})).toBeEnabled());expect(calls.mock.calls.some(c=>c[0]==='installation.configure')).toBe(false);await user.click(screen.getByRole('button',{name:'开始安装'}));
  expect(await screen.findByRole('alert')).toHaveTextContent('duplicate version');
  expect(screen.getByLabelText('版本')).toBeEnabled();await user.clear(screen.getByLabelText('版本'));await user.type(screen.getByLabelText('版本'),'2');rejected=false;
  await user.click(screen.getByRole('button',{name:'开始安装'}));await waitFor(()=>expect(onReady).toHaveBeenCalledWith('saved'));
  const requests=calls.mock.calls.filter(c=>c[0]==='installation.configure').map(c=>c[1]);expect(requests[0].request_id).not.toBe(requests[1].request_id);expect(requests[1].version).toBe('2');
 });
 it('chooses a base before Linux installation and defers all program selection until after installation',async()=>{
  calls.mockImplementation(async(method:string)=>method==='installation.inspect'?{format:'deb',platform:'linux',name:'App',version:'1',entries:[{name:'editor',path:'/usr/bin/editor'},{name:'viewer',path:'/usr/bin/viewer'}],notes:[]}:method==='installation.options'?[{label:'Ubuntu 22',value:'base22'},{label:'Ubuntu 24',value:'base24'},{label:'Ubuntu 26',value:'base26',default:true}]:{id:'saved'});
  const family={id:'f',name:'App',platform:'linux'};const user=view(<InstallationGuide artifactID={'a'.repeat(64)} family={family} onClose={()=>{}} onReady={vi.fn(async()=>{})}/>);
  expect(await screen.findByLabelText('基础环境')).toHaveValue('base26');expect(screen.getByRole('button',{name:'开始安装'})).toBeEnabled();expect(calls.mock.calls.some(c=>c[0]==='installation.configure')).toBe(false);
  expect(screen.queryByLabelText('发现的应用入口')).not.toBeInTheDocument();await user.selectOptions(screen.getByLabelText('基础环境'),'base24');await user.click(screen.getByRole('button',{name:'开始安装'}));
  await waitFor(()=>expect(calls).toHaveBeenCalledWith('installation.configure',expect.objectContaining({base:'base24',entry:''})));
 });
 it('uses local base choices and an atomic stable setup request before reviewing installation',async()=>{
  let attempts=0;calls.mockImplementation(async(method:string)=>{if(method==='installation.inspect')return {format:'appimage',platform:'linux',name:'App',version:'1',entries:[{name:'App',path:'squashfs-root/AppRun'}],notes:[]};if(method==='installation.options')return [{label:'Ubuntu 22.04 · 本地镜像',value:'ubuntu@sha256:fixed'}];if(method==='installation.configure'){if(attempts++===0)throw new Error('response lost');return {id:'saved'}};return []});
  const onReady=vi.fn(async()=>{});const user=view(<InstallationGuide artifactID={'a'.repeat(64)} family={{id:"f",name:"App",platform:"linux"}} onClose={()=>{}} onReady={onReady}/>);
  await waitFor(()=>expect(screen.getByRole('button',{name:'开始安装'})).toBeEnabled());await user.click(screen.getByRole('button',{name:'开始安装'}));
  await screen.findByRole('alert');
  expect(screen.queryByLabelText('配方 JSON')).not.toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('response lost');
  await user.click(screen.getByRole('button',{name:'继续安装'}));await waitFor(()=>expect(onReady).toHaveBeenCalledWith('saved'));
  const setup=calls.mock.calls.filter(x=>x[0]==='installation.configure');expect(setup).toHaveLength(2);expect(setup[0][1]).toEqual(setup[1][1]);expect(calls.mock.calls.some(x=>x[0]==='job.submit')).toBe(false);
 });
 it('asks only which EXE to open for a Windows ZIP',async()=>{
  calls.mockImplementation(async(method:string)=>method==='installation.inspect'?{format:'zip',platform:'windows',name:'App',version:'1',entries:[{name:'setup.exe',path:'folder/setup.exe'},{name:'portable.exe',path:'folder/portable.exe'}],notes:[]}:method==='installation.options'?[{label:'soda',value:'soda'}]:{id:'new'});
  const user=view(<InstallationGuide artifactID={'b'.repeat(64)} family={{id:"f",name:"App",platform:"windows"}} onClose={()=>{}} onReady={vi.fn(async()=>{})}/>);
  const submit=await screen.findByRole('button',{name:'打开所选 EXE'});expect(submit).toBeDisabled();
  expect(screen.queryByLabelText('这个程序的用途')).not.toBeInTheDocument();await user.click(screen.getByText('版本与显示（可选）'));expect(screen.getByLabelText('隐藏额外托盘窗口')).not.toBeChecked();await user.click(screen.getByLabelText('隐藏额外托盘窗口'));await user.selectOptions(screen.getByLabelText('ZIP / EXE 入口'),'folder/portable.exe');await user.click(submit);
  await waitFor(()=>expect(calls).toHaveBeenCalledWith('installation.configure',expect.objectContaining({mode:'installer',entry:'folder/portable.exe',hide_wine_tray:true})));
 });
 it('lets a single EXE use the same optional display settings before creating its environment',async()=>{
  calls.mockImplementation(async(method:string)=>method==='installation.inspect'?{format:'exe',platform:'windows',architecture:'386',name:'',version:'',entries:[{name:'app.exe',path:'app.exe'}],notes:[]}:method==='installation.options'?[{label:'soda',value:'soda'}]:{id:'new'});
  const ready=vi.fn(async()=>{});const user=view(<InstallationGuide artifactID={'c'.repeat(64)} family={{id:'f',name:'App',platform:'windows'}} onClose={()=>{}} onReady={ready}/>);
  const submit=await screen.findByRole('button',{name:'打开所选 EXE'});await waitFor(()=>expect(submit).toBeEnabled());
  expect(calls.mock.calls.some(c=>c[0]==='installation.configure')).toBe(false);
  await user.click(screen.getByText('版本与显示（可选）'));
  expect(screen.getByLabelText('隐藏额外托盘窗口')).not.toBeChecked();
  await user.click(screen.getByLabelText('隐藏额外托盘窗口'));
  await user.selectOptions(screen.getByLabelText('此版本窗口缩放'),'144');
  await user.click(submit);await waitFor(()=>expect(ready).toHaveBeenCalledWith('new'));
  expect(calls.mock.calls.filter(c=>c[0]==='installation.configure')).toHaveLength(1);
  expect(calls).toHaveBeenCalledWith('installation.configure',expect.objectContaining({mode:'installer',entry:'app.exe',version:expect.any(String),dpi:144,hide_wine_tray:true}));
  expect(screen.queryByLabelText('这个程序的用途')).not.toBeInTheDocument();expect(screen.queryByLabelText('ZIP / EXE 入口')).not.toBeInTheDocument();
 });
 it('does not equate installer exit with completion and requires selected EXE plus confirmation',async()=>{
  calls.mockResolvedValue([{name:'Installed App',path:'/tmp/bottle/drive_c/app.exe'}]);const onPlan=vi.fn(async()=>{});const onRun=vi.fn();const user=view(<InstallationSession instance={instance} busy={false} error="" onClose={()=>{}} onPlan={onPlan} onRun={onRun} onStop={()=>{}}/>);
  expect(screen.queryByText(/所选 EXE 已在专属环境中打开/)).not.toBeInTheDocument();expect(screen.getByText(/请确认所选 EXE 的窗口是否出现/)).toBeInTheDocument();
  expect(screen.getByRole('button',{name:'保存应用入口'})).toBeDisabled();await user.click(screen.getByRole('button',{name:'重新打开所选 EXE'}));expect(onRun).toHaveBeenCalledOnce();expect(onPlan).not.toHaveBeenCalled();
  await screen.findByRole('option',{name:'Installed App'});await user.selectOptions(screen.getByLabelText('发现的程序'),'/tmp/bottle/drive_c/app.exe');expect(screen.getByRole('button',{name:'保存应用入口'})).toBeDisabled();await user.click(screen.getByRole('checkbox'));await user.click(screen.getByRole('button',{name:'保存应用入口'}));expect(onPlan).toHaveBeenCalledWith('installation.finish',{id:'i',executable_path:'/tmp/bottle/drive_c/app.exe',cwd:'/tmp/bottle/drive_c',confirmed:true});
 });
 it('keeps entry selection locked while an installer or its descendants are active',async()=>{
  const stop=vi.fn();const user=view(<InstallationSession instance={instance} environment={{id:'e',backend:'bottles-flatpak',name:'zah-i',state:'running',ownership:'managed',launch_lease:true}} busy={false} error="" onClose={()=>{}} onPlan={vi.fn()} onRun={vi.fn()} onStop={stop}/>);
  expect(screen.queryByRole('button',{name:'保存应用入口'})).not.toBeInTheDocument();expect(calls).not.toHaveBeenCalled();await user.click(screen.getByRole('button',{name:'停止此环境'}));expect(stop).toHaveBeenCalledOnce();
 });
 it('requires confirmation even for one installed Linux candidate and never opens a host file picker',async()=>{
  calls.mockResolvedValue([{name:'Installed editor',kind:'desktop',path:'/usr/share/applications/editor.desktop'}]);const onPlan=vi.fn(async()=>{});
  const linux={...instance,installation:{...instance.installation!,mode:'native',format:'deb'}};
  const user=view(<InstallationSession instance={linux} busy={false} error="" onClose={()=>{}} onPlan={onPlan} onRun={()=>{}} onStop={()=>{}}/>);
  await screen.findByRole('option',{name:'Installed editor'});expect(onPlan).not.toHaveBeenCalled();expect(screen.getByRole('button',{name:'保存应用入口'})).toBeDisabled();
  expect(screen.queryByText('手动选择安装后的 EXE')).not.toBeInTheDocument();await user.selectOptions(screen.getByLabelText('发现的程序'),'/usr/share/applications/editor.desktop');await user.click(screen.getByRole('checkbox'));await user.click(screen.getByRole('button',{name:'保存应用入口'}));
  expect(onPlan).toHaveBeenCalledWith('installation.finish',{id:'i',executable_path:'/usr/share/applications/editor.desktop',kind:'desktop',confirmed:true});
 });

});
