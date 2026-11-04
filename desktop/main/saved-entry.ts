/** Only the manager's existing registration can authorize re-verification without
 * a new file dialog. Never turn renderer-supplied paths into reusable grants. */
export async function isSavedEntryRegistration(method:string,params:Record<string,unknown>,read:(method:string,params:Record<string,unknown>)=>Promise<unknown>):Promise<boolean>{
 if(method!=='plan.create'||params.operation!=='instance.register')return false;
 if(Object.keys(params).some(k=>!['operation','input'].includes(k)))return false;
 const input=params.input;
 if(!input||typeof input!=='object'||Array.isArray(input))return false;
 const value=input as Record<string,unknown>;
 if(Object.keys(value).length!==4||Object.keys(value).some(k=>!['id','bottle_path','executable_path','program_name'].includes(k))||typeof value.id!=='string'||!/^[a-f0-9]{32}$/.test(value.id))return false;
 const instance=await read('catalog.get',{kind:'instance',id:value.id}) as {data?:{id?:string;environment_id?:string}};
 if(instance.data?.id!==value.id||!instance.data.environment_id)return false;
 const environment=await read('catalog.get',{kind:'environment',id:instance.data.environment_id}) as {data?:{id?:string;backend?:string;ownership?:string;name?:string;registration?:{bottle?:{path?:string};executable_path?:string;program_name?:string}}};
 const env=environment.data,entry=env?.registration;
 return env?.id===instance.data.environment_id&&env.backend==='bottles-flatpak'&&env.ownership==='managed'&&env.name===`zah-${value.id}`&&!!entry&&typeof entry.bottle?.path==='string'&&typeof entry.executable_path==='string'&&typeof entry.program_name==='string'&&value.bottle_path===entry.bottle.path&&value.executable_path===entry.executable_path&&value.program_name===entry.program_name;
}
