import { createServer as createHttpServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { isIP } from 'node:net';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { hasValidInternalToken, launchProjectFolder, parseProjectFolderMap, resolveProjectFolder, type FolderEditor, type ProjectFolderMap } from './local-folders.js';

const opencodeHealthTimeoutMs = 2_000;

type GatewayOptions = {
  opencodeServerURL?: string;
  fetcher?: typeof fetch;
  internalToken?: string;
  projectFolders?: ProjectFolderMap;
  folderLauncher?: (folderPath: string, editor: FolderEditor) => Promise<void>;
};

type ReadinessFailure = 'not_configured' | 'invalid_configuration' | 'upstream_unhealthy';

export function createServer(options: GatewayOptions = {}): Server {
  const fetcher = options.fetcher ?? fetch;
  const folderLauncher = options.folderLauncher ?? launchProjectFolder;

  return createHttpServer((request, response) => {
    if (request.method === 'GET' && request.url === '/healthz') {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end(JSON.stringify({ status: 'ok', service: 'agent-gateway' }));
      return;
    }

    if (request.method === 'GET' && request.url === '/readyz') {
      void respondWithReadiness(response, options.opencodeServerURL, fetcher);
      return;
    }

    const pathname = new URL(request.url ?? '/', 'http://127.0.0.1').pathname;
    const folderMatch = pathname.match(/^\/internal\/projects\/([^/]+)\/folder$/u);
    if (request.method === 'GET' && folderMatch) {
      void respondWithFolderStatus(response, folderMatch[1]!, request.headers.authorization, options);
      return;
    }

    const openFolderMatch = pathname.match(/^\/internal\/projects\/([^/]+)\/open-folder$/u);
    if (request.method === 'POST' && openFolderMatch) {
      void respondWithOpenFolder(response, request, openFolderMatch[1]!, options, folderLauncher);
      return;
    }

    response.writeHead(404, { 'content-type': 'application/json' });
    response.end(JSON.stringify({ error: 'not_found' }));
  });
}

async function respondWithFolderStatus(
  response: ServerResponse,
  projectID: string,
  authorization: string | undefined,
  options: GatewayOptions,
): Promise<void> {
  if (!options.internalToken || options.internalToken.length < 32) {
    writeJSON(response, 503, { error: 'bridge_not_configured' });
    return;
  }
  if (!hasValidInternalToken(authorization, options.internalToken)) {
    writeJSON(response, 401, { error: 'unauthorized' });
    return;
  }
  const folderPath = options.projectFolders
    ? await resolveProjectFolder(options.projectFolders, projectID)
    : undefined;
  writeJSON(response, 200, { available: folderPath !== undefined });
}

async function respondWithOpenFolder(
  response: ServerResponse,
  request: IncomingMessage,
  projectID: string,
  options: GatewayOptions,
  folderLauncher: (folderPath: string, editor: FolderEditor) => Promise<void>,
): Promise<void> {
  if (!options.internalToken || options.internalToken.length < 32) {
    writeJSON(response, 503, { error: 'bridge_not_configured' });
    return;
  }
  if (!hasValidInternalToken(request.headers.authorization, options.internalToken)) {
    writeJSON(response, 401, { error: 'unauthorized' });
    return;
  }
  const editor = await readFolderEditor(request);
  if (!editor) {
    writeJSON(response, 400, { error: 'invalid_folder_request' });
    return;
  }
  const folderPath = options.projectFolders
    ? await resolveProjectFolder(options.projectFolders, projectID)
    : undefined;
  if (!folderPath) {
    writeJSON(response, 404, { error: 'folder_not_configured' });
    return;
  }
  try {
    await folderLauncher(folderPath, editor);
  } catch {
    writeJSON(response, 503, { error: 'folder_open_failed' });
    return;
  }
  writeJSON(response, 202, { status: 'opening', editor });
}

async function readFolderEditor(request: IncomingMessage): Promise<FolderEditor | undefined> {
  const mediaType = request.headers['content-type']?.split(';', 1)[0]?.trim().toLowerCase();
  if (mediaType !== 'application/json') return undefined;
  const contentLength = Number(request.headers['content-length'] ?? '0');
  if (Number.isFinite(contentLength) && contentLength > 1024) return undefined;

  let body = '';
  for await (const chunk of request) {
    body += Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk);
    if (Buffer.byteLength(body) > 1024) return undefined;
  }
  try {
    const value: unknown = JSON.parse(body);
    if (typeof value !== 'object' || value === null || Array.isArray(value) ||
      Object.keys(value).length !== 1 || !('editor' in value)) return undefined;
    return value.editor === 'explorer' || value.editor === 'vscode' ? value.editor : undefined;
  } catch {
    return undefined;
  }
}

function writeJSON(response: ServerResponse, statusCode: number, payload: unknown): void {
  response.writeHead(statusCode, {
    'cache-control': 'no-store',
    'content-type': 'application/json',
    'x-content-type-options': 'nosniff',
  });
  response.end(JSON.stringify(payload));
}

async function respondWithReadiness(
  response: ServerResponse,
  serverURL: string | undefined,
  fetcher: typeof fetch,
): Promise<void> {
  if (!serverURL?.trim()) {
    writeReadiness(response, 503, 'not_configured');
    return;
  }

  const healthURL = resolveOpenCodeHealthURL(serverURL);
  if (!healthURL) {
    writeReadiness(response, 503, 'invalid_configuration');
    return;
  }

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), opencodeHealthTimeoutMs);
  try {
    const upstream = await fetcher(healthURL, {
      method: 'GET',
      headers: { accept: 'application/json' },
      redirect: 'error',
      signal: controller.signal,
    });
    if (!upstream.ok) {
      writeReadiness(response, 503, 'upstream_unhealthy');
      return;
    }

    const payload: unknown = await upstream.json();
    const healthy = typeof payload === 'object' && payload !== null &&
      'healthy' in payload && payload.healthy === true;
    writeReadiness(response, healthy ? 200 : 503, healthy ? undefined : 'upstream_unhealthy');
  } catch {
    writeReadiness(response, 503, 'upstream_unhealthy');
  } finally {
    clearTimeout(timeout);
  }
}

function resolveOpenCodeHealthURL(serverURL: string): URL | undefined {
  if (serverURL !== serverURL.trim()) return undefined;

  try {
    const server = new URL(serverURL);
    if ((server.protocol !== 'http:' && server.protocol !== 'https:') ||
      server.username || server.password || (server.pathname !== '/' && server.pathname !== '') ||
      server.search || server.hash) {
      return undefined;
    }

    const hostname = server.hostname.replace(/^\[|\]$/gu, '').toLowerCase();
    const loopbackAddress = hostname === 'localhost' || hostname === '::1' ||
      (isIP(hostname) === 4 && hostname.startsWith('127.'));
    if (!loopbackAddress) return undefined;

    return new URL('/global/health', server.origin);
  } catch {
    return undefined;
  }
}

function writeReadiness(
  response: ServerResponse,
  statusCode: 200 | 503,
  reason?: ReadinessFailure,
): void {
  response.writeHead(statusCode, {
    'cache-control': 'no-store',
    'content-type': 'application/json',
    'x-content-type-options': 'nosniff',
  });
  response.end(JSON.stringify(reason
    ? { status: 'not_ready', component: 'opencode', reason }
    : { status: 'ready', component: 'opencode' }));
}

function startServer() {
  const port = Number(process.env.AGENT_GATEWAY_PORT ?? '4100');
  if (!Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new Error('AGENT_GATEWAY_PORT must be an integer from 1 to 65535');
  }

  createServer({
    opencodeServerURL: process.env.OPENCODE_SERVER_URL,
    internalToken: process.env.AGENT_GATEWAY_INTERNAL_TOKEN,
    projectFolders: parseProjectFolderMap(process.env.AGENT_GATEWAY_PROJECT_FOLDERS),
  })
    .listen(port, '127.0.0.1', () => {
      console.info(`agent gateway listening on 127.0.0.1:${port}`);
    });
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  startServer();
}
