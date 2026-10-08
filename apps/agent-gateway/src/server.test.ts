import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { createServer } from './server.js';

async function withServer(run: (baseUrl: string) => Promise<void>, server = createServer()) {
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });

  const address = server.address();
  if (!address || typeof address === 'string') {
    throw new Error('expected a TCP address');
  }

  try {
    await run(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve, reject) => {
      server.close((error) => (error ? reject(error) : resolve()));
    });
  }
}

test('health endpoint identifies the gateway and reports ready', async () => {
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/healthz`);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('content-type'), 'application/json');
    assert.deepEqual(await response.json(), {
      status: 'ok',
      service: 'agent-gateway',
    });
  });
});

test('unknown gateway routes return 404', async () => {
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/not-a-route`);
    assert.equal(response.status, 404);
  });
});

test('ready endpoint fails closed when OpenCode is not configured', async () => {
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/readyz`);
    assert.equal(response.status, 503);
    assert.deepEqual(await response.json(), {
      status: 'not_ready',
      component: 'opencode',
      reason: 'not_configured',
    });
  }, createServer({ opencodeServerURL: '' }));
});

test('ready endpoint checks local OpenCode health without exposing its response', async () => {
  let checkedURL = '';
  let redirectMode: RequestRedirect | undefined;
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/readyz`);
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), {
      status: 'ready',
      component: 'opencode',
    });
    assert.equal(checkedURL, 'http://127.0.0.1:4096/global/health');
    assert.equal(redirectMode, 'error');
  }, createServer({
    opencodeServerURL: 'http://127.0.0.1:4096/',
    fetcher: async (input, init) => {
      checkedURL = String(input);
      redirectMode = init?.redirect;
      return new Response(JSON.stringify({ healthy: true, version: 'private-version' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    },
  }));
});

test('ready endpoint rejects remote and non-origin OpenCode URLs without probing them', async () => {
  for (const opencodeServerURL of [
    'https://example.com',
    'http://localhost:4096/private',
    'http://localhost:4096?probe=1',
  ]) {
    let fetchCalled = false;
    await withServer(async (baseUrl) => {
      const response = await fetch(`${baseUrl}/readyz`);
      assert.equal(response.status, 503, opencodeServerURL);
      assert.deepEqual(await response.json(), {
        status: 'not_ready',
        component: 'opencode',
        reason: 'invalid_configuration',
      });
      assert.equal(fetchCalled, false, opencodeServerURL);
    }, createServer({
      opencodeServerURL,
      fetcher: async () => {
        fetchCalled = true;
        return new Response(JSON.stringify({ healthy: true }), { status: 200 });
      },
    }));
  }
});

test('ready endpoint reports unhealthy upstreams without reflecting their payload', async () => {
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/readyz`);
    assert.equal(response.status, 503);
    assert.deepEqual(await response.json(), {
      status: 'not_ready',
      component: 'opencode',
      reason: 'upstream_unhealthy',
    });
  }, createServer({
    opencodeServerURL: 'http://localhost:4096',
    fetcher: async () => new Response(JSON.stringify({ healthy: false, detail: 'private' }), { status: 200 }),
  }));
});

test('ready endpoint fails closed when the OpenCode health request errors', async () => {
  await withServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/readyz`);
    assert.equal(response.status, 503);
    assert.deepEqual(await response.json(), {
      status: 'not_ready',
      component: 'opencode',
      reason: 'upstream_unhealthy',
    });
  }, createServer({
    opencodeServerURL: 'http://localhost:4096',
    fetcher: async () => { throw new Error('upstream unavailable'); },
  }));
});

test('folder bridge opens only an allowlisted project directory and never returns its path', async (t) => {
  const scratch = process.env.TMPDIR;
  assert.ok(scratch, 'Hermes scratch directory must be configured for filesystem tests');
  const projectRoot = await mkdtemp(join(scratch, 'dioffice-folder-'));
  t.after(() => rm(projectRoot, { recursive: true, force: true }));

  const projectID = '01234567-89ab-4cde-8fab-0123456789ab';
  const token = `test-only-${'x'.repeat(48)}`;
  let opened: { path: string; editor: string } | undefined;
  const server = createServer({
    internalToken: token,
    projectFolders: new Map([[projectID, projectRoot]]),
    folderLauncher: async (path, editor) => { opened = { path, editor }; },
  });

  await withServer(async (baseUrl) => {
    const authorization = { authorization: `Bearer ${token}` };
    const status = await fetch(`${baseUrl}/internal/projects/${projectID}/folder`, { headers: authorization });
    assert.equal(status.status, 200);
    const statusBody = await status.json();
    assert.deepEqual(statusBody, { available: true });
    assert.equal(JSON.stringify(statusBody).includes(projectRoot), false);

    const openedResponse = await fetch(`${baseUrl}/internal/projects/${projectID}/open-folder`, {
      method: 'POST',
      headers: { ...authorization, 'content-type': 'application/json' },
      body: JSON.stringify({ editor: 'vscode' }),
    });
    assert.equal(openedResponse.status, 202);
    assert.deepEqual(await openedResponse.json(), { status: 'opening', editor: 'vscode' });
    assert.deepEqual(opened, { path: projectRoot, editor: 'vscode' });
  }, server);
});

test('folder bridge rejects missing internal auth, arbitrary paths, and unconfigured projects', async (t) => {
  const scratch = process.env.TMPDIR;
  assert.ok(scratch, 'Hermes scratch directory must be configured for filesystem tests');
  const projectRoot = await mkdtemp(join(scratch, 'dioffice-folder-'));
  t.after(() => rm(projectRoot, { recursive: true, force: true }));

  const projectID = '01234567-89ab-4cde-8fab-0123456789ab';
  const token = `test-only-${'x'.repeat(48)}`;
  let opened = false;
  const server = createServer({
    internalToken: token,
    projectFolders: new Map([[projectID, projectRoot]]),
    folderLauncher: async () => { opened = true; },
  });

  await withServer(async (baseUrl) => {
    const unauthenticated = await fetch(`${baseUrl}/internal/projects/${projectID}/folder`);
    assert.equal(unauthenticated.status, 401);

    const arbitraryPath = await fetch(`${baseUrl}/internal/projects/${projectID}/open-folder`, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
      body: JSON.stringify({ editor: 'explorer', path: projectRoot }),
    });
    assert.equal(arbitraryPath.status, 400);

    const unconfigured = await fetch(`${baseUrl}/internal/projects/11234567-89ab-4cde-8fab-0123456789ab/open-folder`, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
      body: JSON.stringify({ editor: 'explorer' }),
    });
    assert.equal(unconfigured.status, 404);
    assert.deepEqual(await unconfigured.json(), { error: 'folder_not_configured' });
    assert.equal(opened, false);
  }, server);
});
