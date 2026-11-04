/** The renderer can only request allowlisted operations through Electron preload. */
import type {ZahBridge} from '../../../desktop/shared/bridge';
export type DesktopBridge = ZahBridge;
declare global { interface Window { zah?:DesktopBridge } }
export function bridge():DesktopBridge { if(!window.zah) throw new Error('请通过 ZaH 桌面程序打开。当前页面未连接本地管理服务。'); return window.zah; }
export async function call<T>(method:string,params:object={}):Promise<T>{
 try{return await bridge().call(method,params) as T}
 catch(value){
  if(value instanceof Error)throw value;
  if(value&&typeof value==='object'&&'message' in value&&typeof value.message==='string'){
   const error=new Error(value.message);
   if('rpcCode' in value&&Number.isInteger(value.rpcCode))Object.assign(error,{rpcCode:value.rpcCode});
   throw error;
  }
  throw new Error('本地操作失败，请检查管理服务连接。');
 }
}
export interface Row<T> {id:string;kind:string;data:T;created_at:string;updated_at:string}
export interface Family {platform?:string;stable_id?:string;id:string;name:string;default_instance_id?:string}
export interface Release {icon_sha256?:string;id:string;family_id:string;platform:string;version:string;arch:string;artifacts:string[]}
export interface Instance {installation?:{hide_wine_tray?:boolean;request_sha256:string;format:string;mode:string;source_entry:string;stage:string};id:string;release_id:string;environment_id:string;name:string;state:string;private_data?:string;favorite:boolean;recipe_revision:string;last_used?:string;original_artifact:string;observed_installed_version:string;drift:string}
export interface Artifact {availability?:string;sha256:string;size:number;format:string;source:{original_name:string;platform:string;arch:string;imported_at:string}}
export interface Environment {registration?:{bottle:{path:string};executable_path:string;program_name:string};stop_requested?:boolean;launch_lease?:boolean;id:string;backend:string;name:string;state:string;ownership:string;pending_restore?:{schema_version:number;backend:string;archive_path:string;archive_sha256:string;image_id?:string;state:string}}
export interface Plan {input?:unknown;instance_id?:string;id:string;operation:string;permissions?:string[];data_impact?:string[];preconditions?:string[];steps:{id:string;description:string;safe_cancel:boolean}[]}
export interface Job {id:string;plan_id:string;state:string;step_index:number;error?:string;result?:unknown;created_at:string;updated_at:string}
export interface FileBinding {restored_data?:string;id:string;instance_id:string;host_path:string;target_path:string;access:string;state:string}
export interface Capability {id:string;instance_id:string;capability:string;state:string;evidence:string;version:string;checked_at:string}
export interface Snapshot {binding?:Row<FileBinding>[];capability?:Row<Capability>[];family:Row<Family>[];release:Row<Release>[];instance:Row<Instance>[];artifact:Row<Artifact>[];environment:Row<Environment>[];job:Row<Job>[]}
export interface BundleInspection {catalog?:{format?:string};manifest:{format:string;schema_version:number;arch:string;resources:{id:string;kind:string;status:string;reason?:string}[];omissions:string[];requirements:string[];offline_ready:boolean};files:number;expanded_bytes:number}
export function rows<T>(value:Row<T>[]|null|undefined):T[]{return (value??[]).map(r=>r.data);}
export const stateNames:Record<string,string>={verified:'已核实',data_removal_pending:'私有数据删除未完成，需检查',forgotten:'登记已删除',needs_runtime_restore:'等待恢复运行环境',restore_pending:'等待运行时恢复',needs_binding:'等待重新绑定',historical_unverified:'历史记录，待验证',registered_running_state_unknown:'已登记，运行状态未知',launcher_exited_application_unknown:'启动器已退出，应用状态未知',stopped:'已停止',passed:'用户记录通过',queued:'排队中',running:'执行中',waiting_user:'等待人工处理',cancel_requested:'等待安全取消',succeeded:'已完成',failed:'失败',cancelled:'已取消',interrupted:'意外中断',unprepared:'待准备',installing:'安装中',needs_inspection:'需要检查',launching:'启动处理中，需检查',ready:'入口已验证',archived:'已归档',removed:'已移除',unknown:'未知',missing:'未找到',changed:'已变化',entry_verified:'入口已验证',awaiting_backend_mapping:'等待配置映射'};

export function applicationActivity(instance:Instance,environment:Environment|undefined,failedStop=false):'launching'|'stopping'|'active'|'unknown'|null{
 if(environment?.stop_requested||environment?.state==='stopping')return failedStop?'unknown':'stopping';
 if(environment?.state==='running')return 'active';
 if(instance.state==='launching'||environment?.state==='launching')return 'launching';
 if(environment?.launch_lease||['stopping','launcher_exited_application_unknown'].includes(environment?.state??''))return 'unknown';
 return null;
}
