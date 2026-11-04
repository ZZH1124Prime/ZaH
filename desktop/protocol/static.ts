import fs from 'node:fs/promises';
import path from 'node:path';

export const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'none'; base-uri 'none'; object-src 'none'; frame-src 'none'; form-action 'none'";
const TYPES: Record<string, string> = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.png': 'image/png',
  '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp', '.ico': 'image/x-icon', '.woff2': 'font/woff2' };

/** Resolve only bundled static assets. URL normalization never hides traversal. */
export async function resolveAsset(root: string, rawURL: string): Promise<{ filename: string; mime: string }> {
  const match = /^zah:\/\/app\/([^?#]*)(?:[?#].*)?$/.exec(rawURL);
  if (!match || /%(2f|5c)/i.test(match[1]!)) throw new Error('Invalid resource URL');
  const decoded = decodeURIComponent(match[1]!);
  if (decoded.includes('\\') || decoded.includes('\0') || decoded.split('/').some(p => p === '..' || p === '.')) throw new Error('Invalid resource path');
  const relative = decoded || 'index.html';
  const mime = TYPES[path.extname(relative).toLowerCase()];
  if (!mime) throw new Error('Unsupported resource type');
  const realRoot = await fs.realpath(root);
  const filename = await fs.realpath(path.join(realRoot, relative));
  if (!filename.startsWith(realRoot + path.sep)) throw new Error('Resource escapes application directory');
  const stat = await fs.stat(filename);
  if (!stat.isFile() || stat.size > 8 * 1024 * 1024) throw new Error('Invalid or oversized resource');
  return { filename, mime };
}

/** Protocol response creation is independent of Electron for headless tests. */
export async function staticResponse(root: string, url: string, method = 'GET'): Promise<Response> {
  const headers = { 'Content-Security-Policy': CSP, 'X-Content-Type-Options': 'nosniff', 'Cache-Control': 'no-cache' };
  if (method !== 'GET' && method !== 'HEAD') return new Response(null, { status: 405, headers });
  try {
    const asset = await resolveAsset(root, url);
    const body = method === 'HEAD' ? null : new Uint8Array(await fs.readFile(asset.filename));
    return new Response(body, { status: 200, headers: { ...headers, 'Content-Type': asset.mime } });
  } catch { return new Response('Resource unavailable', { status: 404, headers }); }
}
