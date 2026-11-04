import {RefreshCw, CircleCheck, CircleAlert} from 'lucide-react';
import {DependencyInstall} from './DependencyInstall';
import {useQuery} from '@tanstack/react-query';
import {call} from '../lib/api';
import {Button,Badge} from '../components/ui/primitives';
function errorText(e:unknown){return e instanceof Error?e.message:String(e)}
interface HostReport {OS:string;Arch:string;Tools:{Name:string;Available:boolean}[];checks:{name:string;available:boolean;detail:string}[]}
export function HostPanel(){
 const q=useQuery({queryKey:['host'],queryFn:()=>call<HostReport>('host.inspect'),staleTime:30000,retry:false});
 const report=q.data;
 const tools=report?.Tools??[];
 const checks=report?.checks??[];
 const missing=tools.filter(t=>!t.Available).map(t=>t.Name);
 return <section className="card host-panel"><div className="section-line"><h2>运行环境</h2><Button disabled={q.isFetching} title="重新检查" aria-label={q.isFetching?"正在检查…":"重新检查"} onClick={()=>void q.refetch()}><RefreshCw size={18}/><span className="sr-only">{q.isFetching?'正在检查…':'重新检查'}</span></Button></div>
 <p className="sr-only" role="status" aria-live="polite">{q.isFetching?'正在检查宿主命令，请稍候。':q.isError?'检查失败，请查看错误后重试。':q.isSuccess?`检查完成 · ${new Date(q.dataUpdatedAt).toLocaleTimeString('zh-CN')} · ${missing.length?`缺少命令：${missing.join('、')}`:'所列命令均已找到'}`:'等待检查。'}</p>
 {q.error&&<p role="alert">{errorText(q.error)}</p>}
 <details><summary>{q.isSuccess&&!missing.length&&!checks.some(c=>!c.available)?<CircleCheck size={17}/>:<CircleAlert size={17}/>} 依赖检查与安装{checks.some(c=>!c.available)?` · ${checks.filter(c=>!c.available).length}`:""}</summary>
 {report&&<><p>{report.OS} · {report.Arch}</p><div className="tool-list">{tools.map(t=><div key={t.Name}><strong>{t.Name}</strong><Badge tone={t.Available?'good':'neutral'}>{t.Available?'找到命令':'未找到命令'}</Badge></div>)}</div></>}
 <div className="tool-list">{checks.map(c=><div key={c.name}><strong>{c.name}</strong><Badge tone={c.available?"good":"bad"}>{c.available?"可用":"需要处理"}</Badge><p>{c.detail}</p></div>)}</div><p className="quiet-note">检查不会安装软件或修改服务、权限。Linux 使用 Docker 和 Distrobox；Windows 使用 Flatpak、Bottles 和对应 Wine 运行器。基础环境检查通过不代表所有应用都兼容。</p><DependencyInstall onComplete={()=>{void q.refetch()}}/></details></section>
}
