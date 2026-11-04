import {it,expect,vi} from 'vitest';
import {render,screen,waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {HostPanel} from './HostPanel';
function view(call:ReturnType<typeof vi.fn>){Object.defineProperty(window,'zah',{value:{call},configurable:true});render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><HostPanel/></QueryClientProvider>);return userEvent.setup()}
it('locks duplicate dependency checks until the original request completes',async()=>{
 let finish!:(value:unknown)=>void;const call=vi.fn(()=>new Promise(resolve=>{finish=resolve}));view(call);
 expect(screen.getByRole('button',{name:'正在检查…'})).toBeDisabled();expect(call).toHaveBeenCalledOnce();
 finish({OS:'linux',Tools:[{Name:'docker',Available:false}]});expect(await screen.findByText(/检查完成 .*缺少命令：docker/)).toBeVisible();expect(screen.getByRole('button',{name:'重新检查'})).toBeEnabled();
});
it('shows check errors without automatic retry and allows manual recovery',async()=>{
 const call=vi.fn().mockRejectedValueOnce(new Error('service unavailable')).mockResolvedValue({OS:'linux',Tools:[]});const user=view(call);
 expect(await screen.findByRole('alert')).toHaveTextContent('service unavailable');expect(call).toHaveBeenCalledOnce();await user.click(screen.getByRole('button',{name:'重新检查'}));await waitFor(()=>expect(screen.queryByRole('alert')).not.toBeInTheDocument());expect(call).toHaveBeenCalledTimes(2);
});
