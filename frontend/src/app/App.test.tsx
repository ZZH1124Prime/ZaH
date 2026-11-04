import {beforeEach,describe,it,expect,vi,type Mock} from 'vitest';
import {render,screen,within,waitFor,fireEvent} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {App} from './App';
import type {Snapshot,DesktopBridge,Job,Plan} from '../lib/api';
let data:Snapshot;let invoke:Mock;let savedPlans:Plan[];let sequence=0;
function mount(path='/'){render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><MemoryRouter initialEntries={[path]}><App/></MemoryRouter></QueryClientProvider>);return userEvent.setup()}
beforeEach(()=>{sequence=0;savedPlans=[];data={family:[],release:[],instance:[],environment:[],artifact:[],job:[]};invoke=vi.fn(async(method,params)=>{
 if(method==='events.resume')return {events:[],next_cursor:0,has_more:false,snapshot:data};
 if(method==='catalog.snapshot')return data;
 if(method==='catalog.list')return params.kind==='plan'?savedPlans.map(p=>({id:p.id,data:p})):[];
 if(method==='host.inspect')return {OS:'linux',Arch:'amd64',Tools:[{Name:'docker',Available:true},{Name:'flatpak',Available:false}]};
 if(method==='plan.create'){const p={id:`p${++sequence}`,operation:params.operation,instance_id:params.input.id,steps:[]};savedPlans.push(p);return p}
 if(method==='job.submit')return {id:'j',state:'succeeded',result:{sha256:'a'.repeat(64)}};
 if(method==='catalog.get'&&params.kind==='instance')return data.instance.find(i=>i.id===params.id);
 if(method==='installation.inspect')return {format:'appimage',platform:'linux',name:'测试',version:'',entries:[{path:'squashfs-root/AppRun',name:'应用'}],notes:[]};
 if(method==='installation.options')return [{value:'ubuntu@sha256:fixed',label:'Ubuntu 22.04'}];
 if(method==='catalog.delete'){if(params.kind==='release')data.release=data.release.filter(r=>r.id!==params.id);if(params.kind==='family')data.family=data.family.filter(f=>f.id!==params.id)}
 return {};
});Object.defineProperty(window,'zah',{value:{call:invoke,acceptPackage:vi.fn(async()=>'/tmp/dropped.exe'),chooseFile:vi.fn(async()=>'/tmp/inert.AppImage'),chooseDirectory:vi.fn(),saveBundle:vi.fn(async()=>'/tmp/test.zahbundle')} satisfies DesktopBridge,configurable:true});});
function seed(state='ready',environment='stopped'){
 const row={created_at:'',updated_at:''};
 data.family=[{...row,id:'f',kind:'family',data:{id:'f',name:'测试应用',platform:'linux'}}];
 data.release=['1','2'].map(v=>({...row,id:`r${v}`,kind:'release',data:{id:`r${v}`,family_id:'f',platform:'linux',version:v,arch:'amd64',artifacts:[]}}));
 data.instance=[{...row,id:'i',kind:'instance',data:{id:'i',release_id:'r1',environment_id:'e',name:'测试应用 1',state,favorite:false,recipe_revision:'recipe',original_artifact:'a',observed_installed_version:'',drift:''}}];
 data.environment=[{...row,id:'e',kind:'environment',data:{id:'e',name:'zah-i',backend:'distrobox-docker',state:environment,ownership:'managed'}}];
}
it('allows cancellation during the current blocking action without enabling another write',async()=>{
 seed('ready','running');const original=invoke.getMockImplementation()!;
 invoke.mockImplementation(async(m,p)=>{
  if(m==='job.submit'){const j:Job={id:'blocking',plan_id:p.plan_id,state:'running',step_index:0,created_at:'',updated_at:''};data.job=[{id:j.id,kind:'job',data:j,created_at:'',updated_at:''}];return {...j}}
  if(m==='catalog.get'&&p.kind==='job')return data.job[0];
  if(m==='job.cancel'){data.job[0]!.data.state='cancelled';return {requested:true}}
  return original(m,p);
 });
 const user=mount();await user.click(await screen.findByRole('button',{name:'停止环境'}));
 await user.click(await screen.findByText('查看操作日志'));
 expect(screen.getByRole('button',{name:'＋ 添加应用'})).toBeDisabled();
 await user.click(await screen.findByRole('button',{name:'取消此操作'}));
 await waitFor(()=>expect(invoke).toHaveBeenCalledWith('job.cancel',{id:'blocking'}));
 expect(await screen.findByRole('alert')).toHaveTextContent('cancelled');
 expect(invoke.mock.calls.filter(c=>c[0]==='job.cancel')).toHaveLength(1);
});
describe('three-page application workflow',()=>{
 it('shows exactly three navigation destinations and no implementation pages or refresh action',async()=>{mount();const nav=screen.getByRole('navigation');expect(within(nav).getAllByRole('link').map(x=>x.textContent)).toEqual(['应用库','管理','迁移']);expect(await screen.findByText('添加你的第一个应用')).toBeVisible();expect(screen.queryByRole('button',{name:'刷新'})).not.toBeInTheDocument();expect(invoke.mock.calls.some(c=>c[0]==='job.submit')).toBe(false)});
 it('creates an application with its selected platform only',async()=>{const user=mount();await user.click(screen.getByRole('button',{name:'＋ 添加应用'}));await user.type(screen.getByLabelText('应用名称'),'画图');await user.selectOptions(screen.getByLabelText('应用平台'),'windows');await user.click(within(screen.getByRole('dialog')).getByRole('button',{name:'添加应用'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('family.create',{name:'画图',platform:'windows'}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false)});
 it('opens the native picker from the chosen card and proceeds to that application without an archive page',async()=>{seed();const user=mount();await user.click(await screen.findByRole('button',{name:'为 测试应用 添加版本'}));expect(await screen.findByRole('dialog')).toHaveTextContent('测试应用 · 安装新版本');expect(screen.queryByLabelText('所属应用')).not.toBeInTheDocument();expect(screen.queryByLabelText('基础环境')).not.toBeInTheDocument();expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'artifact.import',input:{path:'/tmp/inert.AppImage'}})});
 it('picker cancellation does not create a plan and can be reopened',async()=>{seed();vi.mocked(window.zah!.chooseFile).mockResolvedValue(null);const user=mount();const button=await screen.findByRole('button',{name:'为 测试应用 添加版本'});await user.click(button);await user.click(button);expect(window.zah!.chooseFile).toHaveBeenCalledTimes(2);expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false)});
 it('locks the version selector and targets the active version when stopping',async()=>{seed('ready','running');const user=mount();expect(await screen.findByLabelText('测试应用 版本')).toBeDisabled();await user.click(screen.getByRole('button',{name:'停止环境'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.stop',input:{id:'i'}}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create'&&c[1].operation==='instance.launch')).toBe(false)});
 it('running starts only the selected ready instance and does not wait for launcher exit',async()=>{seed();const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>m==='job.submit'?{id:'launch',state:'running'}:original(m,p));const user=mount();await user.click(await screen.findByRole('button',{name:'运行'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.launch',input:{id:'i'}}));expect(invoke.mock.calls.some(c=>c[0]==='catalog.get'&&c[1].kind==='job')).toBe(false)});
 it('canceling stop-all creates no job; confirming scopes it to managed applications',async()=>{const user=mount();await user.click(screen.getByRole('button',{name:'全部停止'}));await user.click(screen.getByRole('button',{name:'取消'}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false);await user.click(screen.getByRole('button',{name:'全部停止'}));await user.click(screen.getByRole('button',{name:'确认停止全部'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'applications.stopAll',input:{confirmed:true}}))});
 it('management exposes application and version selections, canceling deletion changes nothing',async()=>{seed();const user=mount('/manage');await user.click(await screen.findByRole('button',{name:'管理版本'}));const dialog=screen.getByRole('dialog');await user.click(within(dialog).getByRole('checkbox',{name:'全选'}));await user.click(within(dialog).getByRole('button',{name:'删除所选版本'}));const dialogs=screen.getAllByRole('dialog');expect(dialogs.at(-1)).toHaveTextContent('2 个版本');await user.click(within(dialogs.at(-1)!).getByRole('button',{name:'取消'}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false)});
 it('deletion failures retain the catalog and show partial-failure evidence instead of removing optimistically',async()=>{seed('needs_inspection');const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='plan.create'&&p.operation==='instance.remove')throw new Error('identity changed');return original(m,p)});const user=mount('/manage');await user.click(await screen.findByRole('button',{name:'删除应用'}));await user.click(screen.getByRole('button',{name:'确认删除'}));expect(await screen.findByRole('alert')).toHaveTextContent('identity changed');expect(data.family).toHaveLength(1);expect(data.release.some(r=>r.id==='r1')).toBe(true)});
 it('management HOME precheck failure submits no deletion and retains the selected version',async()=>{
  seed();const original=invoke.getMockImplementation()!;
  invoke.mockImplementation(async(m,p)=>{if(m==='plan.create'&&p.operation==='instance.remove')throw new Error('删除预检未通过，尚未删除此应用实例的环境或数据：cannot inspect process cwd: permission denied');return original(m,p)});
  const user=mount('/manage');await user.click(await screen.findByRole('button',{name:'管理版本'}));await user.click(screen.getAllByRole('button',{name:'删除此版本'})[0]!);
  expect(screen.getAllByRole('dialog').at(-1)).toHaveTextContent('Bottle（含 C: 盘内文件）');
  await user.click(screen.getByRole('button',{name:'确认删除'}));
  expect(await screen.findByRole('alert')).toHaveTextContent('尚未删除');
  expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.remove',input:{id:'i',precheck_private_data:true}});
  expect(invoke.mock.calls.some(c=>['job.submit','catalog.delete'].includes(c[0]))).toBe(false);
  expect(data.release.some(r=>r.id==='r1')).toBe(true);expect(data.environment[0]!.data.state).toBe('stopped');
 });
 it('shows a completed dependency check including missing commands',async()=>{const user=mount('/manage');expect(await screen.findByText(/检查完成 .*缺少命令：flatpak/)).toBeVisible();await user.click(screen.getByRole('button',{name:'重新检查'}));await waitFor(()=>expect(invoke.mock.calls.filter(c=>c[0]==='host.inspect')).toHaveLength(2));expect(invoke.mock.calls.some(c=>c[0]==='job.submit')).toBe(false)});
});

it('blocks duplicate run submission and supports keyboard activation',async()=>{seed();const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='job.submit'){const j={id:'j',plan_id:p.plan_id,state:'queued',step_index:0,created_at:'',updated_at:''};data.job=[{id:'j',kind:'job',data:j,created_at:'',updated_at:''}];return j}return original(m,p)});const user=mount();const button=await screen.findByRole('button',{name:'运行'});button.focus();await user.keyboard('{Enter}{Enter}');await waitFor(()=>expect(invoke.mock.calls.filter(c=>c[0]==='job.submit')).toHaveLength(1));expect(screen.getByRole('button',{name:'正在启动'})).toBeDisabled()});
it('keeps icons version-specific and permits launch when icon lookup fails',async()=>{seed();data.release.forEach(r=>r.data.icon_sha256='a'.repeat(64));data.instance.push({...data.instance[0]!,id:'i2',data:{...data.instance[0]!.data,id:'i2',release_id:'r2',environment_id:'e2'}});const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='release.icon.get')throw new Error('image unavailable');return original(m,p)});const user=mount();await user.selectOptions(await screen.findByLabelText('测试应用 版本'),'r2');await waitFor(()=>expect(invoke).toHaveBeenCalledWith('release.icon.get',{id:'r2'}));expect(screen.getByRole('button',{name:'运行'})).toBeEnabled();await user.click(screen.getByRole('button',{name:'设置 测试应用 当前版本图标'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('release.icon.set',{id:'r2',path:'/tmp/inert.AppImage'}))});
it('a Windows started launcher remains stoppable and does not block a different app',async()=>{seed('ready','running');data.family[0]!.data.platform='windows';data.release[0]!.data.platform='windows';data.environment[0]!.data.backend='bottles-flatpak';data.family.push({...data.family[0]!,id:'other',data:{id:'other',name:'其他应用',platform:'linux'}});data.release.push({...data.release[0]!,id:'other-r',data:{...data.release[0]!.data,id:'other-r',family_id:'other',platform:'linux'}});data.instance.push({...data.instance[0]!,id:'other-i',data:{...data.instance[0]!.data,id:'other-i',release_id:'other-r',environment_id:'other-e'}});const user=mount();expect(await screen.findByRole('button',{name:'停止环境'})).toBeEnabled();await user.click(screen.getByRole('button',{name:'运行'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.launch',input:{id:'other-i'}}))});
it('unknown leased environment locks versions and offers stop instead of install verification',async()=>{seed('needs_inspection','needs_inspection');data.environment[0]!.data.launch_lease=true;mount();expect(await screen.findByRole('button',{name:'停止环境'})).toBeEnabled();expect(screen.getByLabelText('测试应用 版本')).toBeDisabled();expect(screen.queryByRole('button',{name:'检查安装结果'})).not.toBeInTheDocument()});
it('failed Linux installation offers verification without replaying the installer',async()=>{seed('needs_inspection');const user=mount();await user.click(await screen.findByRole('button',{name:'检查安装结果'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.verify',input:{id:'i'}}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create'&&c[1].operation==='instance.install')).toBe(false)});
it('restored data never makes an unverified environment launchable',async()=>{seed('needs_runtime_restore','restore_pending');mount();expect(await screen.findByRole('status',{name:'待恢复'})).toBeVisible();expect(screen.queryByRole('button',{name:'运行'})).not.toBeInTheDocument();expect(screen.getByRole('button',{name:'恢复环境'})).toBeEnabled();expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false)});
it('persisted batch job locks new actions while allowing cancellation of that job',async()=>{seed();savedPlans=[{id:'batch',operation:'applications.stopAll',steps:[]}];data.job=[{id:'job',kind:'job',created_at:'',updated_at:'',data:{id:'job',plan_id:'batch',state:'running',step_index:0,created_at:'',updated_at:''}}];const user=mount();await waitFor(()=>expect(screen.getByRole('button',{name:'全部停止'})).toBeDisabled());expect(screen.getByRole('button',{name:'运行'})).toBeDisabled();await user.click(screen.getByText('查看操作日志'));await user.click(screen.getByRole('button',{name:'取消此操作'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('job.cancel',{id:'job'}));expect(screen.getByText('执行中')).toBeVisible()});
it('inspects archive before conservative merging and reports pending runtime accurately',async()=>{const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='bundle.inspect')return{catalog:{format:'zah.instance-environment'},manifest:{arch:'amd64',requirements:[]}};return original(m,p)});vi.mocked(window.zah!.chooseDirectory).mockResolvedValue('/tmp/private-target');const user=mount('/migration');await user.click(screen.getByRole('button',{name:'导入 ZaH 迁移包'}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false);await user.click(await screen.findByRole('button',{name:'确认导入'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'environment.restore',input:{path:'/tmp/inert.AppImage',merge:true,directory:'/tmp/private-target'}}));await user.click(screen.getByText('查看操作日志'));expect((await screen.findAllByText(/数据导入完成，跳过/))[0]).toBeVisible()});
it('invalid bundle and cancelled restore directory never create an import job',async()=>{let valid=false;const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='bundle.inspect'){if(!valid)throw new Error('checksum mismatch');return{catalog:{format:'zah.instance-environment'},manifest:{arch:'amd64',requirements:[]}}}return original(m,p)});vi.mocked(window.zah!.chooseDirectory).mockResolvedValue(null);const user=mount('/migration');await user.click(screen.getByRole('button',{name:'导入 ZaH 迁移包'}));expect(await screen.findByRole('alert')).toHaveTextContent('checksum mismatch');valid=true;await user.click(screen.getByRole('button',{name:'导入 ZaH 迁移包'}));await user.click(await screen.findByRole('button',{name:'确认导入'}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create')).toBe(false)});
it('restoration retry uses the saved environment and never launches or repeats import',async()=>{seed('needs_runtime_restore','restore_pending');const user=mount();await user.click(await screen.findByRole('button',{name:'恢复环境'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'environment.rebuild',input:{id:'i'}}));expect(invoke.mock.calls.some(c=>c[0]==='plan.create'&&['instance.launch','instance.install','environment.restore'].includes(c[1].operation))).toBe(false)});
it('full import rebuilds only newly imported instances and keeps partial failures visible',async()=>{const original=invoke.getMockImplementation()!;invoke.mockImplementation(async(m,p)=>{if(m==='bundle.inspect')return {catalog:{format:'zah.instance-environment'},manifest:{arch:'amd64',requirements:[]}};if(m==='job.submit'){const plan=savedPlans.find(v=>v.id===p.plan_id)!;if(plan.operation==='environment.restore')return {id:'import',state:'succeeded',result:{id_map:{'family:old':'f','instance:a':'first','instance:b':'second'},skipped_versions:1}};if(plan.instance_id==='first')return {id:'restore-one',state:'failed',error:'runner unavailable'};return {id:'restore-two',state:'succeeded'}}return original(m,p)});vi.mocked(window.zah!.chooseDirectory).mockResolvedValue('/tmp/private-target');const user=mount('/migration');await user.click(screen.getByRole('button',{name:'导入 ZaH 迁移包'}));await user.click(await screen.findByRole('button',{name:'确认导入'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'environment.rebuild',input:{id:'second'}}));await user.click(screen.getByText('查看操作日志'));expect(screen.getAllByText('first：runner unavailable')[0]).toBeVisible();expect(invoke.mock.calls.filter(c=>c[0]==='plan.create'&&c[1].operation==='environment.restore')).toHaveLength(1);expect(invoke.mock.calls.filter(c=>c[0]==='plan.create'&&c[1].operation==='environment.rebuild')).toHaveLength(2);expect(invoke.mock.calls.some(c=>c[0]==='plan.create'&&c[1].operation==='instance.launch')).toBe(false)});

it('a dropped local package uses the same import flow without opening another picker',async()=>{
 seed();mount();const card=await screen.findByRole('article',{name:'测试应用 应用'});
 const file=new File(['fixture'],'test.exe');fireEvent.drop(card,{dataTransfer:{files:[file]}});
 await waitFor(()=>expect(window.zah!.acceptPackage).toHaveBeenCalledWith(file));
 expect(window.zah!.chooseFile).not.toHaveBeenCalled();expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'artifact.import',input:{path:'/tmp/dropped.exe'}});
});

it('keeps a single Stop action after the Linux launcher exits',async()=>{
 seed('ready','launcher_exited_application_unknown');data.environment[0]!.data.launch_lease=true;
 const user=mount();expect(await screen.findByRole('button',{name:'停止环境'})).toBeEnabled();expect(screen.getByLabelText('测试应用 版本')).toBeDisabled();
 expect(screen.queryByRole('button',{name:'重新打开'})).not.toBeInTheDocument();expect(screen.queryByRole('button',{name:'运行'})).not.toBeInTheDocument();
 await user.click(screen.getByRole('button',{name:'停止环境'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.stop',input:{id:'i'}}));
 expect(invoke.mock.calls.some(c=>c[0]==='plan.create'&&c[1].operation==='instance.launch')).toBe(false);
});

it('reverifies a migrated Windows entry from its card using its saved paths',async()=>{
 seed();data.family[0]!.data.platform='windows';data.release.forEach(r=>r.data.platform='windows');
 data.environment[0]!.data.registration={bottle:{path:'/saved/bottle'},executable_path:'/saved/bottle/drive_c/app.exe',program_name:'Saved App'};
 const user=mount();await user.click(await screen.findByLabelText(/更多操作/));await user.click(screen.getByRole('button',{name:'重新验证已保存入口'}));
 await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',{operation:'instance.register',input:{id:'i',bottle_path:'/saved/bottle',executable_path:'/saved/bottle/drive_c/app.exe',program_name:'Saved App'}}));
 expect(invoke.mock.calls.some(c=>c[0]==='installation.configure'||c[0]==='catalog.delete')).toBe(false);
});
it('does not reverify an active Windows application',async()=>{
 seed('running','running');data.family[0]!.data.platform='windows';data.release.forEach(r=>r.data.platform='windows');
 data.environment[0]!.data.registration={bottle:{path:'/saved/bottle'},executable_path:'/saved/bottle/drive_c/app.exe',program_name:'Saved App'};
 const user=mount();await user.click(await screen.findByLabelText(/更多操作/));expect(screen.getByRole('button',{name:'重新验证已保存入口'})).toBeDisabled();
});
it('offers recovery for a rejected launch without an installation session',async()=>{
 seed('launching','launching');data.family[0]!.data.platform='windows';data.release.forEach(r=>r.data.platform='windows');
 data.environment[0]!.data.launch_lease=true;
 data.environment[0]!.data.registration={bottle:{path:'/saved/bottle'},executable_path:'/saved/bottle/drive_c/app.exe',program_name:'Saved App'};
 savedPlans=[{id:'failed-plan',operation:'instance.launch',instance_id:'i',steps:[]}];
 data.job=[{id:'failed-launch',kind:'job',created_at:'',updated_at:'',data:{id:'failed-launch',plan_id:'failed-plan',state:'failed',step_index:0,created_at:'2026-11-04T00:00:00Z',updated_at:'',result:{state:'launch_not_started'}}}];
 const user=mount();await user.click(await screen.findByLabelText(/更多操作/));const button=screen.getByRole('button',{name:'重新验证已保存入口'});await waitFor(()=>expect(button).toBeEnabled());await user.click(button);
 await waitFor(()=>expect(invoke).toHaveBeenCalledWith('plan.create',expect.objectContaining({operation:'instance.register'})));
});
