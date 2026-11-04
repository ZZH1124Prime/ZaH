// Code generated from schemas/rpc.json; DO NOT EDIT.
export const PROTOCOL_VERSION = 1 as const;
export const MAX_FRAME_BYTES = 1048576;
export interface ApplicationsStopAllInput {
  confirmed: true;
}
export interface ApplicationsStopAllPlanParams {
  input: ApplicationsStopAllInput;
  operation: "applications.stopAll";
}
export interface CatalogRecord {
  created_at: string;
  data: unknown;
  id: string;
  kind: string;
  updated_at: string;
}
export interface RPCError {
  code: number;
  data?: unknown;
  message: string;
}
export interface Event {
  created_at: string;
  data: unknown;
  seq: number;
  type: string;
}
export interface EventPage {
  events: Event[];
  has_more: boolean;
  next_cursor: number;
  snapshot?: Record<string, CatalogRecord[]>;
}
export interface HelloParams {
  version: 1;
}
export interface HelloResult {
  max_connections: 32;
  max_frame_bytes: 1048576;
  version: 1;
}
export interface InstanceStopInput {
  id: string;
}
export interface InstanceStopPlanParams {
  input: InstanceStopInput;
  operation: "instance.stop";
}
export interface ReleaseIconSetParams {
  id: string;
  path: string;
}
export interface Request {
  id?: string | number | null;
  jsonrpc: "2.0";
  method: string;
  params?: Record<string, unknown> | unknown[];
}
export interface Response {
  error?: RPCError;
  id: string | number | null;
  jsonrpc: "2.0";
  result?: unknown;
}
export interface ResumeParams {
  after?: number | null;
  limit?: number;
}
