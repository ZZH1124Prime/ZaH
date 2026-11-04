import { contextBridge, ipcRenderer, webUtils } from 'electron';
import type { ZahBridge } from '../shared/bridge';

// Sandboxed preload imports only Electron at runtime, never Node or local files.
const bridge: ZahBridge = Object.freeze({
  call: <T = unknown>(method: string, params: Record<string, unknown> = {}): Promise<T> =>
    ipcRenderer.invoke('zah:call', method, params).then((reply: unknown) => {
      if (!reply || typeof reply !== 'object' || !('ok' in reply)) throw new Error('Invalid local response');
      const envelope = reply as {ok: boolean; value?: T; error?: string; rpcCode?: number};
      if (envelope.ok !== true) {
        // Error custom properties are discarded across contextBridge. Reject
        // with cloneable data; the renderer client constructs its local Error.
        throw {message:typeof envelope.error === 'string' ? envelope.error : 'Local operation failed',
          ...(Number.isInteger(envelope.rpcCode)?{rpcCode:envelope.rpcCode}:{})};
      }
      return envelope.value as T;
    }),
  acceptPackage: (file: File): Promise<string> => {
    const selected = webUtils.getPathForFile(file);
    if (!selected) return Promise.reject(new Error('请拖入本机实际存在的安装包。'));
    return ipcRenderer.invoke('zah:accept-package', selected);
  },
  chooseFile: (options?: {instanceID:string}): Promise<string | null> => ipcRenderer.invoke('zah:choose-file',options),
  chooseDirectory: (): Promise<string | null> => ipcRenderer.invoke('zah:choose-directory'),
  saveBundle: (format?: 'plain' | 'encrypted'): Promise<string | null> => ipcRenderer.invoke('zah:save-bundle', format)
});
contextBridge.exposeInMainWorld('zah', bridge);
