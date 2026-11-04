import {useRef,useState} from 'react';
import {useQuery} from '@tanstack/react-query';
import {Button,Modal} from '../components/ui/primitives';
import {applicationActivity,bridge,call,type Family,type Instance,type Environment} from '../lib/api';
interface Media {architecture?:string;format:string;platform:string;name:string;version:string;entries:{name:string;path:string}[];notes:string[]}
interface Option {label:string;value:string;default?:boolean}
export function InstallationGuide({artifactID,family,onClose,onReady}:{artifactID:string;family:Family;onClose:()=>void;onReady:(id:string)=>Promise<void>}){
 const id=useRef(crypto.randomUUID().replaceAll('-',''));const importedVersion=useRef(new Date().toISOString());const frozen=useRef<Record<string,string|number|boolean>|null>(null);
 const [version,setVersion]=useState<string|undefined>(),[base,setBase]=useState(''),[entry,setEntry]=useState(''),[dpi,setDpi]=useState(String(Math.max(96,Math.min(384,Math.round((window.devicePixelRatio||1)*96))))),[hideWineTray,setHideWineTray]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[attempted,setAttempted]=useState(false);
 const media=useQuery({queryKey:['installation-media',artifactID],queryFn:()=>call<Media>('installation.inspect',{artifact_id:artifactID}),retry:false});
 const options=useQuery({queryKey:['installation-options',media.data?.platform],queryFn:()=>call<Option[]>('installation.options',{platform:media.data!.platform}),enabled:!!media.data,retry:false});
 const m=media.data,selectedBase=base||(m?.platform==='linux'?options.data?.find(o=>o.default)?.value:'')||((m?.platform==='windows'||options.data?.length===1)?options.data?.[0]?.value:'')||'',selectedEntry=m?.platform==='linux'?(m.format==='appimage'?'squashfs-root/AppRun':''):entry||(m?.format==='exe'?m.entries[0]?.path:'')||'';
 async function submit(){if(busy||!m)return;setBusy(true);setError('');try{
  if(family.platform&&family.platform!==m.platform)throw new Error('所选安装包的平台与应用平台不一致，请选择对应的安装包。');
  if(!frozen.current)frozen.current={request_id:id.current,artifact_id:artifactID,family_id:family.id,name:family.name,version:version??(m.version||(m.platform==='windows'?importedVersion.current:'')),base:selectedBase,entry:selectedEntry,mode:m.platform==='linux'?'native':'installer',...(m.platform==='windows'?{dpi:Number(dpi),...(hideWineTray?{hide_wine_tray:true}:{})}:{})};
  setAttempted(true);const instance=await call<Instance>('installation.configure',frozen.current);await onReady(instance.id);
 }catch(e){
  // Only the manager's pre-transaction rejection permits a new request.
  // Lost replies and storage failures retain the frozen idempotent request.
  if(e&&typeof e==='object'&&'rpcCode' in e&&e.rpcCode===-32014){frozen.current=null;id.current=crypto.randomUUID().replaceAll('-','');setAttempted(false)}
  setError(e instanceof Error?e.message:String(e))
 }finally{setBusy(false)}}
 return <Modal title={`${family.name} · 安装新版本`} busy={busy} error={error||String(media.error?.message||options.error?.message||'')} onClose={onClose}>
 {media.isPending?<p role="status">正在检查安装包…</p>:m&&<form onSubmit={e=>{e.preventDefault();void submit()}}>
 <p>{m.platform==='linux'?'Linux':'Windows'} · {m.format.toUpperCase()} · {m.architecture==='386'?'x86（32 位）':m.architecture??'amd64'}</p>
 <fieldset className="installation-fields" disabled={busy||attempted}>
 {m.platform==='linux'&&<label>版本<input required maxLength={128} value={version??m.version} onChange={e=>setVersion(e.target.value)}/></label>}
 {m.platform==='linux'&&((options.data?.length??0)>1)&&<label>{m.platform==='linux'?'基础环境':'已安装的 Wine runner'}<select required value={selectedBase} onChange={e=>setBase(e.target.value)}><option value="">请选择</option>{options.data?.map(o=><option key={o.value} value={o.value}>{o.label}</option>)}</select></label>}
 {options.isPending&&<p role="status">正在检查本地环境选项…</p>}
 {(options.error||options.data?.length===0)&&<p className="message">{m.platform==='linux'?'没有可用的本地 Ubuntu 22.04/24.04/26.04 镜像。请先准备 Docker/Distrobox 和官方基础镜像；请到管理页检查依赖。':'没有受支持的本地 runner。请先安装 Flatpak 版 Bottles 并在其中准备普通 Wine runner。此处不会安装系统依赖或更改权限。'}</p>}
 {m.platform==='windows'&&m.format==='zip'&&<label>ZIP / EXE 入口<select required value={selectedEntry} onChange={e=>setEntry(e.target.value)}><option value="">请选择要打开的 EXE</option>{m.entries.map(e=><option key={e.path} value={e.path}>{e.name||e.path} · {e.path}</option>)}</select></label>}
 {m.platform==='windows'&&<details><summary>版本与显示（可选）</summary><label>版本<input required maxLength={128} value={version??(m.version||importedVersion.current)} onChange={e=>setVersion(e.target.value)}/></label><label>此版本窗口缩放<select value={dpi} onChange={e=>setDpi(e.target.value)}><option value="0">使用 Wine 默认</option>{[96,144,192,240,288,384].map(value=><option key={value} value={value}>{value/96*100}%（{value} DPI）</option>)}</select></label><label className="checkbox"><input type="checkbox" checked={hideWineTray} onChange={e=>setHideWineTray(e.target.checked)}/>隐藏额外托盘窗口</label><p className="quiet-note">仅在应用可通过启动入口重新打开时使用；需要托盘菜单的应用请保留。</p></details>}
 </fieldset>
 {options.error&&<Button type="button" disabled={busy||options.isFetching} onClick={()=>void options.refetch()}>重新检查环境选项</Button>}
 {m.platform==='windows'?<p className="quiet-note">{m.format==='zip'?'ZIP 保留完整目录和相邻文件。':''}无需区分安装器和便携程序。选中的 EXE 会在专属环境中打开；安装选项由你操作，最后选择并保存以后要运行的应用入口。</p>:<p className="quiet-note">将自动准备此版本的专属环境，完成后由你选择安装好的程序。deb 的维护脚本、服务和 AppImage 对宿主功能的要求仍取决于应用；不保证任意软件兼容。</p>}
 {attempted&&<p className="quiet-note">已保留安装进度。重试不会重复创建环境；也可以返回后从应用卡片继续安装。</p>}
 <div className="form-actions"><Button type="button" disabled={busy} onClick={onClose}>返回</Button><Button variant="primary" disabled={busy||!selectedBase||m.platform==='windows'&&!selectedEntry}>{busy?'正在安装…':attempted?'继续安装':m.platform==='windows'?'打开所选 EXE':'开始安装'}</Button></div>
 </form>}
 </Modal>
}
interface Candidate {name:string;path:string;kind?:string;cwd?:string;args?:string[];arguments?:string}
export function InstallationSession({instance,environment,busy,error,onClose,onPlan,onRun,onStop}:{instance:Instance;environment?:Environment;busy:boolean;error:string;onClose:()=>void;onPlan:(operation:string,input:object)=>Promise<void>;onRun:()=>void;onStop:()=>void}){
 const [selected,setSelected]=useState(''),[confirmed,setConfirmed]=useState(false),[fileBusy,setFileBusy]=useState(false),[localError,setLocalError]=useState(''),[args,setArgs]=useState('');
 const setup=instance.installation!,windows=setup.mode!=='native',activity=applicationActivity(instance,environment);
 // A Linux container may be running after installation without an application
 // lease. Candidate discovery is permitted then; Windows must first be stopped.
 const entryBusy=windows?!!activity:!!environment?.launch_lease||instance.state==='launching'||instance.state==='running';
 const candidates=useQuery({queryKey:['installation-programs',instance.id,instance.state,setup.stage,environment?.state],queryFn:()=>call<Candidate[]>('installation.programs',{id:instance.id}),enabled:instance.state==='ready'&&!entryBusy,retry:false});
 const chosen=candidates.data?.find(p=>p.path===selected);
 function select(path:string){setSelected(path);setConfirmed(false);setArgs((candidates.data?.find(p=>p.path===path)?.args??[]).join('\n'))}
 const cwd=chosen?.cwd||(windows&&selected?selected.slice(0,selected.lastIndexOf('/')):'');
 return <Modal title={`${instance.name} · 安装与入口`} busy={busy||fileBusy} error={error||localError} onClose={onClose}>
 {instance.state==='unprepared'?<><p>点击继续安装，将自动准备此版本的专属环境。</p><Button variant="primary" disabled={busy} onClick={()=>void onPlan('instance.install',{id:instance.id})}>继续安装</Button></>:!['ready','launching','running'].includes(instance.state)?<p className="message">当前状态：{instance.state}。请查看操作日志中的错误。已保留部分安装，不会在未知状态下重复执行。</p>:<>
 {windows&&<p>{setup.stage==='complete'?'已经保存了应用入口，以后从卡片直接运行。':setup.mode==='installer'?'环境已准备。请确认所选 EXE 的窗口是否出现；如有安装选项，由你操作。若没有窗口，请先检查启动结果。完成后停止此环境，再选择以后要运行的应用入口。':'完整目录及依赖已保留。请选择并确认实际要运行的程序。'}</p>}
 {entryBusy?<><p className="message">{environment?.state==='launcher_exited_application_unknown'?'启动调用已结束，尚未确认应用是否仍在运行。':'启动任务进行中，尚未确认应用窗口是否出现。'}结束后停止这个专属环境，再保存入口；已写入的数据保留，未保存的内容会丢失。</p><Button variant="danger" disabled={busy||activity==='launching'||activity==='stopping'} onClick={onStop}>停止此环境</Button></>:<>
 {windows&&setup.mode==='installer'&&setup.stage!=='complete'&&<Button variant="primary" disabled={busy} onClick={onRun}>重新打开所选 EXE</Button>}
 {!windows&&<div className="toolbar"><Button disabled={busy} onClick={()=>void onPlan('installation.tools',{id:instance.id,action:'start'})}>打开环境以选择程序</Button><Button disabled={busy} onClick={()=>void onPlan('installation.tools',{id:instance.id,action:'install'})}>查看安装窗口</Button><Button disabled={busy} onClick={()=>void onPlan('installation.tools',{id:instance.id,action:'packages'})}>管理已安装软件</Button></div>}
 {windows&&<div className="toolbar"><Button disabled={busy} onClick={()=>void onPlan('installation.tools',{id:instance.id,action:'files'})}>浏览此环境文件</Button><Button disabled={busy} onClick={()=>void onPlan('installation.tools',{id:instance.id,action:'uninstall'})}>打开卸载窗口</Button></div>}
 <h3>选择以后运行的应用</h3>
 {candidates.isFetching&&<p role="status">正在读取此环境已安装的程序…</p>}
 <label className="standalone-label">发现的程序<select disabled={busy||candidates.isFetching} value={selected} onChange={e=>select(e.target.value)}><option value="">请选择应用入口</option>{candidates.data?.map((p,i)=><option key={p.name+i} value={p.path}>{p.name}</option>)}</select></label>
 {candidates.error&&<p role="alert">无法读取程序列表：{candidates.error.message}{windows?'。仍可手动选择此环境内的 EXE。':''}</p>}
 <Button disabled={busy||candidates.isFetching} onClick={()=>void candidates.refetch()}>重新读取已安装程序</Button>
 {windows&&<Button disabled={busy||fileBusy} onClick={()=>{setFileBusy(true);setLocalError('');void bridge().chooseFile({instanceID:instance.id}).then(path=>{if(path)select(path)}).catch(e=>setLocalError(String(e))).finally(()=>setFileBusy(false))}}>手动选择安装后的 EXE</Button>}
 {selected&&<><p className="quiet-note">已选入口：{selected}</p>{cwd&&<p className="quiet-note">工作目录：{cwd}</p>}</>}
 {windows&&selected&&<details><summary>启动参数（可选）</summary>{chosen?.arguments&&<p className="quiet-note">发现的原始参数：{chosen.arguments}。请确认下方参数后保存。</p>}<label>每行一个参数<textarea value={args} onChange={e=>{setArgs(e.target.value);setConfirmed(false)}} maxLength={8192}/></label><p className="quiet-note">工作目录使用 EXE 所在目录；不同目录的程序需要额外适配，不会偷偷替换。</p></details>}
 <label className="checkbox"><input type="checkbox" checked={confirmed} onChange={e=>setConfirmed(e.target.checked)}/>{windows?'我已停止环境，确认这个 EXE 是以后要运行的应用。':'我确认这个已安装程序是以后要运行的应用。'}</label>
 <Button variant="primary" disabled={busy||!selected||!confirmed} onClick={()=>void onPlan('installation.finish',{id:instance.id,executable_path:selected,confirmed:true,...(windows?{...(cwd?{cwd}:{}),...(args?{args:args.split('\n')}:{})}:{kind:chosen?.kind})})}>保存应用入口</Button>
 {windows&&setup.stage==='complete'&&setup.mode==='installer'&&<div className="detail-section"><p>需要继续安装或维护时，可以重新打开最初选择的 EXE。</p><Button disabled={busy} onClick={()=>void onPlan('installation.maintenance',{id:instance.id,confirmed:true})}>打开原 EXE</Button></div>}
 </>}
 </>}
 </Modal>
}
