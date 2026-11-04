import test from 'node:test';
import assert from 'node:assert/strict';
import {isSavedEntryRegistration} from '../main/saved-entry';
const id='a'.repeat(32),envID='b'.repeat(32);
const input={id,bottle_path:'/saved/bottle',executable_path:'/saved/bottle/app.exe',program_name:'Saved'};
const read=async(_method:string,params:Record<string,unknown>)=>params.kind==='instance'?{data:{id,environment_id:envID}}:{data:{id:envID,name:`zah-${id}`,backend:'bottles-flatpak',ownership:'managed',registration:{bottle:{path:input.bottle_path},executable_path:input.executable_path,program_name:input.program_name}}};
test('only exact managed saved entry can skip fresh file dialogs',async()=>{
 assert.equal(await isSavedEntryRegistration('plan.create',{operation:'instance.register',input},read),true);
 for(const changed of [{...input,bottle_path:'/other'},{...input,executable_path:'/other.exe'},{...input,program_name:'Other'},{...input,extra:true},{...input,id:'invalid'}])assert.equal(await isSavedEntryRegistration('plan.create',{operation:'instance.register',input:changed},read),false);
 assert.equal(await isSavedEntryRegistration('plan.create',{operation:'instance.register',input},async()=>({data:{}})),false);
 assert.equal(await isSavedEntryRegistration('plan.create',{operation:'artifact.import',input},read),false);
});
