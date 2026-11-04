/** Narrow renderer contract; no filesystem, shell, socket or ipcRenderer access. */
export interface ZahBridge {
  call<T = unknown>(method: string, params?: object): Promise<T>;
  acceptPackage(file: File): Promise<string>;
  chooseFile(options?: {instanceID: string}): Promise<string | null>;
  chooseDirectory(): Promise<string | null>;
  saveBundle(format?: 'plain' | 'encrypted'): Promise<string | null>;
}
