import {useEffect,useRef,useState} from 'react';
import {MessageSquareText,AlertCircle,Copy,Check,X} from 'lucide-react';
import {Button} from '../components/ui/primitives';
import {stateNames,type Job,type Plan} from '../lib/api';
import {OperationRunner} from '../lib/operations';
import {CancelJobButton} from './CancelJobButton';

export function OperationProgress({busy,jobs,plans,log,runner,perform,refresh,report,onError,error,onRetry}:{
 error?:string;onRetry?:()=>void;busy:boolean;jobs:Job[];plans:Plan[];log:string[];runner:OperationRunner;
 perform:(work:()=>Promise<void>)=>Promise<void>;refresh:()=>Promise<unknown>;
 report:(message:string)=>void;onError:(message:string)=>void;
}){
 const [toast,setToast]=useState<string|null>(null),[copyState,setCopyState]=useState('');
 const [lastFailure,setLastFailure]=useState(''),[unreadFailure,setUnreadFailure]=useState(false);
 const details=useRef<HTMLDetailsElement>(null),errorText=useRef<HTMLParagraphElement>(null),mounted=useRef(Date.now());
 const previous=useRef({error:'',states:new Map<string,string>()});
 useEffect(()=>{
  const old=previous.current;let next='';
  for(const job of jobs){
   const state=old.states.get(job.id);old.states.set(job.id,job.state);
   if(state===job.state||!state&&Date.parse(job.created_at)<mounted.current)continue;
   if(job.state==='failed'||job.state==='interrupted')next=job.error||'操作未完成，请查看日志。';
  }
  if(error&&error!==old.error)next=error;
  old.error=error||'';
  if(!next)return;
  setToast(next);setCopyState('');setLastFailure(next);setUnreadFailure(true);report(`错误：${next}`);
 },[jobs,error,report]);
 async function copyError(){
  if(!toast)return;
  try {await navigator.clipboard.writeText(toast);setCopyState('已复制')}
  catch {const selection=window.getSelection();if(selection&&errorText.current){const range=document.createRange();range.selectNodeContents(errorText.current);selection.removeAllRanges();selection.addRange(range);setCopyState(document.execCommand('copy')?'已复制':'已选中，按 Ctrl+C 复制')}}
 }
 function openLog(){if(details.current)details.current.open=true;setUnreadFailure(false)}
 const active=jobs.filter(j=>['queued','running','cancel_requested','waiting_user','interrupted'].includes(j.state));
 const unsubmitted=plans.filter(p=>!jobs.some(j=>j.plan_id===p.id));
 const failed=jobs.filter(j=>j.state==='failed').slice(-3);
 return <section className="operation-progress" aria-label="当前操作">
  {toast&&<div className="operation-toast error" role="alert">
   <AlertCircle size={20}/>
   <div><p ref={errorText} tabIndex={0}>{toast}</p><div className="toast-actions"><button className="toast-log-link" onClick={()=>void copyError()}>{copyState==='已复制'?<Check size={14}/>:<Copy size={14}/>}复制错误</button><button className="toast-log-link" onClick={openLog}>查看日志</button></div>{copyState&&<small role="status">{copyState}</small>}</div>
   <button className="toast-close" title="关闭" aria-label="关闭错误提示" onClick={()=>setToast(null)}><X size={16}/></button>
  </div>}
  <details ref={details} onToggle={e=>{if(e.currentTarget.open)setUnreadFailure(false)}}><summary aria-label="查看操作日志" title={busy?'正在处理…':'操作日志'}><MessageSquareText size={21}/>{(busy||active.length>0||unreadFailure)&&<span className={`log-dot ${unreadFailure?"error":""}`}/>}<span className="sr-only">查看操作日志</span></summary><div className="log-popover"><p role="status">{busy?log.at(-1)||'正在处理…':active.length?`${active.length} 项操作待处理`:log.at(-1)||'操作日志'}</p><div className="operation-log">{lastFailure&&<p className="latest-operation-error">{lastFailure}</p>}{onRetry&&<Button disabled={busy} onClick={onRetry}>重试操作</Button>}
   {unsubmitted.map(p=><div key={p.id}><p>上次操作尚未看到提交结果。</p>
    <Button disabled={busy} onClick={()=>void perform(async()=>{await runner.resume(p);report('已恢复原操作，不会重复执行。')})}>继续原操作</Button>
    <Button disabled={busy} onClick={()=>void perform(async()=>{await runner.discard(p.id);report('已放弃尚未执行的操作。')})}>放弃未执行操作</Button>
   </div>)}
   {log.map((line,i)=><p key={i}>{line}</p>)}
   {[...active,...failed].map(j=><div key={j.id}><p>{stateNames[j.state]??j.state}{j.error?`：${j.error}`:''}</p><SavedJobLog job={j}/>
    {['queued','running','waiting_user'].includes(j.state)&&<CancelJobButton id={j.id} onChanged={refresh} onReport={report} onError={onError}/>}
   </div>)}
  </div></div></details>
 </section>;
}

function SavedJobLog({job}:{job:Job}){
 const value=job.result as {phase?:unknown;lines?:unknown}|undefined;
 if(!Array.isArray(value?.lines))return null;
 return <div role="log">{typeof value.phase==='string'&&<p>{value.phase}</p>}{value.lines.filter((v):v is string=>typeof v==='string').slice(-160).map((line,i)=><p key={i}>{line}</p>)}</div>;
}
