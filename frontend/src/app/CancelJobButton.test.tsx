import {it,expect,vi} from 'vitest';
import {render,screen,waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {CancelJobButton} from './CancelJobButton';

it('deduplicates cancellation while awaiting the control response and permits retry after failure',async()=>{
 let reject!:(e:Error)=>void;
 const invoke=vi.fn(()=>new Promise((_resolve,failed)=>{reject=failed}));
 Object.defineProperty(window,'zah',{value:{call:invoke},configurable:true});
 const onError=vi.fn(),onChanged=vi.fn(async()=>{});
 render(<CancelJobButton id="job" onChanged={onChanged} onReport={()=>{}} onError={onError}/>);
 const user=userEvent.setup();await user.dblClick(screen.getByRole('button',{name:'取消此操作'}));
 expect(invoke).toHaveBeenCalledTimes(1);expect(screen.getByRole('button',{name:'正在请求取消'})).toBeDisabled();
 reject(new Error('connection lost'));await waitFor(()=>expect(onError).toHaveBeenCalledWith('connection lost'));
 expect(onChanged).not.toHaveBeenCalled();expect(screen.getByRole('button',{name:'取消此操作'})).toBeEnabled();
});
