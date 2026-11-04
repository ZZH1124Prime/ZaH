import { useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { call, type Snapshot, type Row, type Plan } from '../lib/api';
import { CatalogEventReader, requestEvents } from '../lib/events';

export function useCatalogEvents(){
 const qc=useQueryClient();
 const reader=useRef<CatalogEventReader|null>(null);
 if(!reader.current)reader.current=new CatalogEventReader();
 return useQuery({
  queryKey:['catalog-events'],
  queryFn:()=>reader.current!.poll(requestEvents,async(snapshot)=>{
   // An older fallback/focus read must not overwrite this acknowledged page.
   await Promise.all(['snapshot','plans'].map(key=>qc.cancelQueries({queryKey:[key],exact:true})));
   if(snapshot!==undefined)qc.setQueryData(['snapshot'],snapshot);
   await Promise.all([
    ...(snapshot===undefined?[qc.fetchQuery({queryKey:['snapshot'],queryFn:()=>call<Snapshot>('catalog.snapshot'),staleTime:0})]:[]),
    qc.fetchQuery({queryKey:['plans'],queryFn:()=>call<Row<Plan>[]>('catalog.list',{kind:'plan'}),staleTime:0}),
   ]);
  }),
  refetchInterval:query=>query.state.status==='error'?false:2000,
  refetchIntervalInBackground:false,
  refetchOnWindowFocus:query=>query.state.status!=='error',
  retry:false,
 });
}
