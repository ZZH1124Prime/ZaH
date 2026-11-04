import {useRef,useState} from 'react';
import {Button} from '../components/ui/primitives';
import {call} from '../lib/api';

// Cancellation is a separate control request. It must remain available while
// the initiating UI action is waiting for that same job to finish.
export function CancelJobButton({id,onChanged,onReport,onError}:{id:string;onChanged:()=>Promise<unknown>;onReport:(message:string)=>void;onError:(message:string)=>void}){
 const pending=useRef(false),[busy,setBusy]=useState(false);
 async function cancel(){
  if(pending.current)return;
  pending.current=true;setBusy(true);
  try{await call('job.cancel',{id});onReport('已请求安全取消，已完成的文件和安装步骤会保留。');await onChanged()}
  catch(e){onError(e instanceof Error?e.message:String(e))}
  finally{pending.current=false;setBusy(false)}
 }
 return <Button disabled={busy} onClick={()=>void cancel()}>{busy?'正在请求取消':'取消此操作'}</Button>;
}
