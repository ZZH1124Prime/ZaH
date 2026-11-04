import {CancelJobButton} from './CancelJobButton';
import {executingStates} from '../lib/task-state';
import {useRef,useState} from 'react';
import {useQuery} from '@tanstack/react-query';
import {call,type Job,type Plan,type Row} from '../lib/api';
import {Button} from '../components/ui/primitives';
const linuxHelp = `# Docker（若尚未安装）
sudo apt-get --no-remove --no-upgrade --no-install-recommends install docker.io
# Distrobox（Ubuntu 20.04 软件源可能没有此包）
git clone --depth 1 --branch 1.8.2.5 https://github.com/89luca89/distrobox.git "$HOME/distrobox-1.8.2.5"
cd "$HOME/distrobox-1.8.2.5"
# 阅读官方安装脚本后运行；仅安装到当前用户目录
less install
sh install --prefix "$HOME/.local"
# 重新打开 ZaH，然后检查 Docker 当前用户访问权限
docker info`;
const windowsHelp = `python3 "$(dirname "$(readlink -f "$HOME/.local/bin/zah")")/scripts/prepare-windows.py" --install`;
const active=(j?:Job)=>!!j&&executingStates.has(j.state);
export function DependencyInstall({onComplete}:{onComplete:()=>void}){
 const [platform,setPlatform]=useState('linux'),[busy,setBusy]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[jobID,setJobID]=useState('');
 const lock=useRef(false),pending=useRef<{id:string;key:string}|null>(null),reported=useRef('');
 const q=useQuery({queryKey:['dependency-job',jobID],enabled:!!jobID,retry:false,queryFn:async()=>{const r=await call<Row<Job>>('catalog.get',{kind:'job',id:jobID});if(!active(r.data)&&reported.current!==jobID){reported.current=jobID;onComplete()}return r.data},refetchInterval:q=>active(q.state.data)?500:false});
 async function install(){if(lock.current||active(q.data)||jobID&&(!q.data||q.isError))return;lock.current=true;setBusy(true);setError('');setMessage('正在检查可安装的依赖…');try{
  if(!pending.current){const p=await call<Plan>('plan.create',{operation:'host.dependencies.install',input:{platform}});pending.current={id:p.id,key:crypto.randomUUID().replaceAll('-','')}}
  const p=pending.current;const result=await call<Job|{cancelled:true}>('host.dependencies.confirm',{plan_id:p.id,idempotency_key:p.key});
  if('cancelled'in result){pending.current=null;await call('plan.discard',{id:p.id});setMessage('已取消，未提交安装。');return}
  setJobID(result.id);pending.current=null;setMessage('安装任务已提交。系统可能显示管理员授权窗口；ZaH 不收集密码。');
 }catch(e){setError(e instanceof Error?e.message:String(e));setMessage(pending.current?'请求结果待确认；重试会查询原操作，不会重复安装。':'未提交安装，请处理错误后重试。')}finally{lock.current=false;setBusy(false)}}
 const progress=q.data?.result as {lines?:string[];phase?:string;truncated?:boolean}|undefined;
 const working=busy||!!jobID&&(!q.data||q.isError)||active(q.data);
 return <div className="dependency-install"><div className="toolbar"><label>安装依赖的平台<select aria-label="安装依赖的平台" disabled={working||!!pending.current} value={platform} onChange={e=>setPlatform(e.target.value)}><option value="linux">Linux</option><option value="windows">Windows</option></select></label><Button disabled={working} onClick={()=>void install()}>{busy?'准备安装计划…':active(q.data)?'依赖安装处理中…':pending.current?'重试原安装请求':'安装缺少的依赖'}</Button></div>

 {platform==='windows'&&<p className="quiet-note">Ubuntu 20.04 使用私有 Flatpak、Bottles 和 Soda。首次准备需要联网及编译工具；失败请查看下方日志中的具体命令。此操作不保证所有应用兼容。</p>}
 {message&&<p role="status">{message}</p>}{(error||q.error||q.data?.error)&&<p role="alert">{error||(q.error instanceof Error?q.error.message:'')||q.data?.error}</p>}
 {(error||q.data?.error)&&<details><summary>手动处理命令</summary><p className="quiet-note">按具体错误选择执行。请先检查命令；安装完成后正常关闭并重新打开 ZaH。不会自动修改代理、软件源或 Docker 用户权限。</p><pre className="operation-log">{platform==='linux'?linuxHelp:windowsHelp}</pre></details>}
 {q.isError&&<Button onClick={()=>void q.refetch()}>重试读取安装状态</Button>}
 {q.data&&<><p role="status">{progress?.phase??q.data.state}</p>{['queued','running'].includes(q.data.state)&&<CancelJobButton id={jobID} onChanged={()=>q.refetch()} onReport={setMessage} onError={setError}/>}<p className="quiet-note">已开始的软件安装事务会继续到安全结束，不强制终止包管理器；取消不会卸载已完成的软件。</p></>}
 {progress?.lines&&<details open><summary>安装日志{progress.truncated?'（保留最近记录）':''}</summary><div className="operation-log" role="log">{progress.lines.map((line,i)=><p key={i}>{line}</p>)}</div></details>}
 </div>
}
