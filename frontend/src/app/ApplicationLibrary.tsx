import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Library, Play, Square, Plus, Search, MoreHorizontal, Wrench, RotateCcw, LoaderCircle, PackageOpen } from 'lucide-react';
import { Button, Badge, Empty } from '../components/ui/primitives';
import { call, applicationActivity, type Family, type Release, type Instance, type Environment } from '../lib/api';

interface Props {
 rejectedLaunchIDs?:string[];
 onReverify?:(instance:Instance)=>void;
 onSetup:(instance:Instance)=>void;
 onIcon:(release:Release)=>void;failedStopIDs:string[];pendingStopIDs:string[];onStop:(instance:Instance)=>void;pendingInstanceIDs:string[];families:Family[];releases:Release[];instances:Instance[];environments:Environment[];
 loading:boolean;busy:boolean;
 onAdd:()=>void;onImport:(family:Family,file?:File)=>void;onRun:(instance:Instance)=>void;
}
export function ApplicationLibrary({rejectedLaunchIDs=[],onReverify,onSetup,onIcon,failedStopIDs,pendingStopIDs,onStop,pendingInstanceIDs,families,releases,instances,environments,loading,busy,onAdd,onImport,onRun}:Props){
 const [search,setSearch]=useState('');
 const visible=families.filter(f=>f.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()));
 const active=families.filter(f=>instances.some(i=>releases.some(r=>r.id===i.release_id&&r.family_id===f.id)&&(pendingInstanceIDs.includes(i.id)||applicationActivity(i,environments.find(e=>e.id===i.environment_id))))).length;
 return <>
  <header className="page-header library-header"><div><h1>应用库</h1></div><div className="toolbar"><Button disabled={busy} title="添加应用" aria-label="＋ 添加应用" onClick={onAdd}><Plus size={20}/><span className="sr-only">添加应用</span></Button></div></header>
  <div className="library-tools"><div className="library-count"><Library size={18}/><strong>{families.length}</strong> 个应用{active>0&&<Badge tone="active">{active} 个正在运行或等待确认</Badge>}</div><label className="search-field"><Search size={17}/><input aria-label="搜索应用" value={search} onChange={e=>setSearch(e.target.value)} placeholder="搜索应用"/></label></div>
  {loading?<p role="status">正在读取应用库…</p>:families.length===0?<Empty title="添加你的第一个应用">选择名称和平台，再点击应用卡片上的加号选择安装包。</Empty>:visible.length===0?<Empty title="没有找到这个应用">试试其他名称，或清空搜索条件。</Empty>:<div className="application-grid">{visible.map(f=><ApplicationCard rejectedLaunchIDs={rejectedLaunchIDs} onReverify={onReverify} onAddVersion={file=>onImport(f,file)} onSetup={onSetup} failedStopIDs={failedStopIDs} onIcon={onIcon} pendingStopIDs={pendingStopIDs} onStop={onStop} pendingInstanceIDs={pendingInstanceIDs} key={f.id} family={f} releases={releases.filter(r=>r.family_id===f.id)} instances={instances} environments={environments} busy={busy} onRun={onRun}/>)}</div>}

 </>;
}
function ApplicationCard({rejectedLaunchIDs,onReverify,onAddVersion,onSetup,onIcon,failedStopIDs,pendingStopIDs,onStop,pendingInstanceIDs,family,releases,instances,environments,busy,onRun}:{rejectedLaunchIDs:string[];onReverify?:(instance:Instance)=>void;onAddVersion:(file?:File)=>void;onSetup:(instance:Instance)=>void;onIcon:(release:Release)=>void;failedStopIDs:string[];pendingStopIDs:string[];onStop:(instance:Instance)=>void;pendingInstanceIDs:string[];family:Family;releases:Release[];instances:Instance[];environments:Environment[];busy:boolean;onRun:(instance:Instance)=>void}){
 const [dropError,setDropError]=useState('');
 const familyInstances=instances.filter(i=>releases.some(r=>r.id===i.release_id));
 const preferred=familyInstances.find(i=>i.id===family.default_instance_id);
 const [chosen,setChosen]=useState(preferred?.release_id??releases[0]?.id??'');
 const activeInstance=familyInstances.find(i=>pendingStopIDs.includes(i.id)||pendingInstanceIDs.includes(i.id)||applicationActivity(i,environments.find(e=>e.id===i.environment_id)));
 const release=releases.find(r=>r.id===(activeInstance?.release_id??chosen))??releases[0];
 const icon=useQuery({queryKey:['release-icon',release?.id,release?.icon_sha256],queryFn:()=>call<string>('release.icon.get',{id:release!.id}),enabled:!!release?.icon_sha256,staleTime:Infinity,retry:false});
 const [failedIcon,setFailedIcon]=useState('');
 const copies=familyInstances.filter(i=>i.release_id===release?.id).sort((a,b)=>a.id.localeCompare(b.id));
 const instance=copies.find(i=>i.id===family.default_instance_id)??copies.find(i=>i.state==='ready')??copies[0];
 const activity=activeInstance?(pendingStopIDs.includes(activeInstance.id)?'stopping':applicationActivity(activeInstance,environments.find(e=>e.id===activeInstance.environment_id),failedStopIDs.includes(activeInstance.id))??(pendingInstanceIDs.includes(activeInstance.id)?'launching':null)):null;
 const recover=instance?.state==='needs_inspection'&&release?.platform==='linux'&&!activeInstance;
 const restore=instance?.state==='needs_runtime_restore'&&!activeInstance;
 const setup=restore||recover||!!instance?.installation&&(instance.installation.stage!=='complete'||instance.state==='unprepared');
 const canStop=!!activeInstance&&(activity==='active'||activity==='unknown');
 const status=activity==='stopping'?'正在停止':activity==='launching'?'正在启动':activity==='active'?'运行中':activity==='unknown'?'运行状态待确认':restore?'待恢复':recover?'需要检查':setup?'安装待完成':instance?.state==='ready'?'可运行':'尚未就绪';
 return <article className={`application-card ${activity?`is-${activity}`:""}`} aria-label={`${family.name} 应用`} onDragOver={e=>{if(e.dataTransfer.types.includes('Files')){e.preventDefault();e.dataTransfer.dropEffect=busy?'none':'copy'}}} onDrop={e=>{e.preventDefault();if(busy)return;if(e.dataTransfer.files.length!==1){setDropError('一次拖入一个安装包。');return}setDropError('');onAddVersion(e.dataTransfer.files[0])}}>
  {dropError&&<p role="alert">{dropError}</p>}
  <div className="application-heading"><button className="application-avatar icon-picker" type="button" disabled={busy||!release} aria-label={`设置 ${family.name} 当前版本图标`} title="选择本地 PNG 图标（仅当前版本）" onClick={()=>{if(release)onIcon(release)}}>{icon.data&&failedIcon!==icon.data?<img src={icon.data} alt={`${family.name} ${release?.version} 图标`} onError={()=>setFailedIcon(icon.data!)}/>:<PackageOpen size={48} aria-hidden/>}</button><div><h2>{family.name}</h2></div><Button variant="quiet" disabled={busy} title="添加版本" aria-label={`为 ${family.name} 添加版本`} onClick={()=>onAddVersion()}><Plus size={19}/></Button><span className={`app-status ${activity||"idle"}`} role="status" aria-label={status} title={status}/></div>
  <div className="application-controls"><label className="version-select"><span className="sr-only">版本</span><select title={release?.version} aria-label={`${family.name} 版本`} disabled={busy||!!activeInstance||!releases.length} value={release?.id??''} onChange={e=>setChosen(e.target.value)}>{!releases.length&&<option value="">尚未安装版本</option>}{releases.map(r=><option key={r.id} value={r.id}>{r.version||'版本未知'} · {r.platform==='linux'?'Linux':'Windows'}</option>)}</select></label><Button className={`run-button ${activity==='stopping'||activity==='launching'?"pending":""}`} title={canStop?"停止环境（未保存数据会丢失）":status} variant={canStop?"danger":"primary"} disabled={busy||(!canStop&&(!!activity||!setup&&instance?.state!=='ready'))} onClick={()=>{if(canStop&&activeInstance)onStop(activeInstance);else if(instance){if(setup)onSetup(instance);else onRun(instance)}}}>{activity==='stopping'||activity==='launching'?<LoaderCircle size={18} className="spin"/>:canStop?<Square size={16} fill="currentColor"/>:<Play size={16} fill="currentColor"/>}{activity==='stopping'?'正在停止':activity==='launching'?'正在启动':canStop?'停止环境':restore?'恢复环境':recover?'检查安装结果':setup?'继续安装':'运行'}</Button></div>
  {(instance?.installation||instance&&release?.platform==='windows'&&environments.find(e=>e.id===instance.environment_id)?.registration&&onReverify)&&<details className="card-menu"><summary aria-label={`${family.name} 更多操作`} title="更多操作"><MoreHorizontal size={20}/></summary><div className="card-menu-panel">
  {!!instance?.installation&&<Button variant="quiet" disabled={busy} onClick={()=>onSetup(instance)}><Wrench size={16}/>安装与入口</Button>}
  {instance&&release?.platform==='windows'&&environments.find(e=>e.id===instance.environment_id)?.registration&&onReverify&&<Button variant="quiet" disabled={busy||(!rejectedLaunchIDs.includes(instance.id)&&(!!activeInstance||instance.state!=='ready'))} onClick={()=>onReverify(instance)}><RotateCcw size={16}/>重新验证已保存入口</Button>}
  </div></details>}

 </article>;
}
