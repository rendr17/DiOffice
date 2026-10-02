import { createServer as createHttpServer, type Server } from 'node:http';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

export function createServer(): Server {
  return createHttpServer((request, response) => {
    if (request.method === 'GET' && request.url === '/healthz') {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end(JSON.stringify({ status: 'ok', service: 'agent-gateway' }));
      return;
    }

    response.writeHead(404, { 'content-type': 'application/json' });
    response.end(JSON.stringify({ error: 'not_found' }));
  });
}

function startServer() {
  const port = Number(process.env.AGENT_GATEWAY_PORT ?? '4100');
  if (!Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new Error('AGENT_GATEWAY_PORT must be an integer from 1 to 65535');
  }

  createServer().listen(port, '127.0.0.1', () => {
    console.info(`agent gateway listening on 127.0.0.1:${port}`);
  });
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  startServer();
}
