import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ApiClient, ApiError, readCookieValue } from './api';

const ok = (body: unknown) => Response.json(body);

test('reads authenticated event history with a decimal replay cursor and builds a cookie-only stream URL', async () => {
  const requests: { url: string; init?: RequestInit }[] = [];
  const client = new ApiClient('http://127.0.0.1:8080/', async (input, init) => {
    requests.push({ url: String(input), init });
    return ok({ items: [], nextCursor: '42', hasMore: false });
  });
  const readEvents = Reflect.get(client, 'listEvents') as ((project: string, after?: string) => Promise<unknown>) | undefined;
  const streamURL = Reflect.get(client, 'eventStreamURL') as ((project: string, after: string) => string) | undefined;
  assert.equal(typeof readEvents, 'function');
  assert.equal(typeof streamURL, 'function');
  assert.deepEqual(await readEvents!.call(client, 'project/1'), { items: [], nextCursor: '42', hasMore: false });
  await readEvents!.call(client, 'project/1', '41');
  assert.equal(requests[0].url, 'http://127.0.0.1:8080/api/v1/projects/project%2F1/events');
  assert.equal(requests[1].url, 'http://127.0.0.1:8080/api/v1/projects/project%2F1/events?after=41&limit=100');
  assert.equal(requests[1].init?.credentials, 'include');
  assert.equal(requests[1].init?.cache, 'no-store');
  assert.equal(streamURL!.call(client, 'project/1', '42'), 'http://127.0.0.1:8080/api/v1/projects/project%2F1/events/stream?after=42');
});

test('reads the CSRF cookie without confusing it with adjacent cookies', () => {
  assert.equal(readCookieValue('dioffice_csrf', 'theme=forest; dioffice_csrf=csrf-token; other=value'), 'csrf-token');
  assert.equal(readCookieValue('missing', 'dioffice_csrf=csrf-token'), undefined);
});

test('sends session cookies and the double-submit token for task creation', async () => {
  let requestURL = '';
  let requestInit: RequestInit | undefined;
  const fetcher: typeof fetch = async (input, init) => {
    requestURL = String(input);
    requestInit = init;
    return ok({ id: 'task-1', status: 'DRAFT' });
  };
  const client = new ApiClient('http://localhost:8080/', fetcher, () => 'dioffice_csrf=csrf-token');

  await client.createTask('project/1', {
    assigneeEmployeeId: 'employee-1',
    title: 'Prepare launch brief',
    description: 'Draft the first version.',
    acceptanceCriteria: ['Visible in project tasks'],
    requiredChecks: [],
    taskType: 'feature',
    priority: 'NORMAL',
  }, 'create-key-1');

  assert.equal(requestURL, 'http://localhost:8080/api/v1/projects/project%2F1/tasks');
  assert.equal(requestInit?.credentials, 'include');
  const headers = new Headers(requestInit?.headers);
  assert.equal(headers.get('X-CSRF-Token'), 'csrf-token');
  assert.equal(headers.get('Idempotency-Key'), 'create-key-1');
  assert.deepEqual(JSON.parse(String(requestInit?.body)), {
    assigneeEmployeeId: 'employee-1',
    title: 'Prepare launch brief',
    description: 'Draft the first version.',
    acceptanceCriteria: ['Visible in project tasks'],
    requiredChecks: [],
    taskType: 'feature',
    priority: 'NORMAL',
  });
});

test('sends an idempotent, version-guarded Owner command to save a draft to backlog', async () => {
  let requestURL = '';
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('http://localhost:8080', async (input, init) => {
    requestURL = String(input);
    requestInit = init;
    return ok({ id: 'task-1', status: 'BACKLOG', version: 4 });
  }, () => 'dioffice_csrf=csrf-token');

  await client.saveTaskToBacklog('project/1', 'task/1', 3, 'backlog-key-1');

  assert.equal(requestURL, 'http://localhost:8080/api/v1/projects/project%2F1/tasks/task%2F1/backlog');
  assert.equal(requestInit?.method, 'POST');
  assert.equal(requestInit?.credentials, 'include');
  assert.equal(requestInit?.cache, 'no-store');
  const headers = new Headers(requestInit?.headers);
  assert.equal(headers.get('Content-Type'), 'application/json');
  assert.equal(headers.get('X-CSRF-Token'), 'csrf-token');
  assert.equal(headers.get('Idempotency-Key'), 'backlog-key-1');
  assert.deepEqual(JSON.parse(String(requestInit?.body)), { expectedVersion: 3 });
});

test('reads project-folder availability without receiving a local path', async () => {
  let requestURL = '';
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('http://localhost:8080', async (input, init) => {
    requestURL = String(input);
    requestInit = init;
    return ok({ available: true });
  });

  assert.deepEqual(await client.getProjectFolderStatus('project/1'), { available: true });
  assert.equal(requestURL, 'http://localhost:8080/api/v1/projects/project%2F1/folder');
  assert.equal(requestInit?.method, undefined);
  assert.equal(requestInit?.credentials, 'include');
  assert.equal(requestInit?.cache, 'no-store');
});

test('sends a CSRF-protected fixed-editor command to open the configured project folder', async () => {
  let requestURL = '';
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('http://localhost:8080', async (input, init) => {
    requestURL = String(input);
    requestInit = init;
    return Response.json({ status: 'opening', editor: 'vscode' }, { status: 202 });
  }, () => 'dioffice_csrf=csrf-token');

  await client.openProjectFolder('project/1', 'vscode');

  assert.equal(requestURL, 'http://localhost:8080/api/v1/projects/project%2F1/folder/open');
  assert.equal(requestInit?.method, 'POST');
  assert.equal(requestInit?.credentials, 'include');
  const headers = new Headers(requestInit?.headers);
  assert.equal(headers.get('Content-Type'), 'application/json');
  assert.equal(headers.get('X-CSRF-Token'), 'csrf-token');
  assert.deepEqual(JSON.parse(String(requestInit?.body)), { editor: 'vscode' });
});

test('sends reference images as multipart without setting a JSON content type', async () => {
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('', async (_input, init) => {
    requestInit = init;
    return ok({ id: 'task-1', status: 'DRAFT' });
  }, () => 'dioffice_csrf=csrf-token');
  const image = new File(['test-pixels'], 'reference.png', { type: 'image/png' });

  await client.createTask('project-1', {
    assigneeEmployeeId: 'employee-1',
    title: 'Implement referenced design',
    description: 'Use the attached visual reference.',
    acceptanceCriteria: [],
    requiredChecks: [],
    taskType: 'feature',
    priority: 'NORMAL',
  }, 'create-with-image', [image]);

  const headers = new Headers(requestInit?.headers);
  assert.equal(headers.get('X-CSRF-Token'), 'csrf-token');
  assert.equal(headers.get('Idempotency-Key'), 'create-with-image');
  assert.equal(headers.has('Content-Type'), false);
  assert.ok(requestInit?.body instanceof FormData);
  const form = requestInit?.body as FormData;
  assert.deepEqual(JSON.parse(String(form.get('task'))), {
    assigneeEmployeeId: 'employee-1',
    title: 'Implement referenced design',
    description: 'Use the attached visual reference.',
    acceptanceCriteria: [],
    requiredChecks: [],
    taskType: 'feature',
    priority: 'NORMAL',
  });
  assert.equal((form.get('referenceImages') as File).name, 'reference.png');
});

test('does not send a protected mutation without a CSRF token', async () => {
  let called = false;
  const fetcher: typeof fetch = async () => {
    called = true;
    return ok({ status: 'signed_out' });
  };
  const client = new ApiClient('http://localhost:8080', fetcher, () => '');

  await assert.rejects(client.logout(), (error: unknown) => error instanceof ApiError && error.code === 'csrf_token_missing');
  assert.equal(called, false);
});

test('keeps login available before a CSRF cookie exists and includes credentials', async () => {
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('http://localhost:8080', async (_input, init) => {
    requestInit = init;
    return ok({ user: { id: 'user-1' } });
  }, () => '');

  await client.login({ organizationId: 'org-1', email: 'owner@example.invalid', password: 'test-only-password' });

  assert.equal(requestInit?.credentials, 'include');
  assert.equal(new Headers(requestInit?.headers).has('X-CSRF-Token'), false);
});

test('converts API error bodies to safe typed errors', async () => {
  const client = new ApiClient('http://localhost:8080', async () => Response.json({ error: 'unauthorized' }, { status: 401 }), () => '');

  await assert.rejects(client.getSession(), (error: unknown) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.status, 401);
    assert.equal(error.code, 'unauthorized');
    return true;
  });
});

test('requests a local development session without sending credentials', async () => {
  let requestURL = '';
  let requestInit: RequestInit | undefined;
  const client = new ApiClient('http://127.0.0.1:18080', async (input, init) => {
    requestURL = String(input);
    requestInit = init;
    return ok({ user: { id: 'dev-user', role: 'OWNER' } });
  }, () => '');
  const createDevelopmentSession = Reflect.get(client, 'createDevelopmentSession') as (() => Promise<unknown>) | undefined;

  assert.equal(typeof createDevelopmentSession, 'function');
  await createDevelopmentSession!.call(client);

  assert.equal(requestURL, 'http://127.0.0.1:18080/api/v1/auth/dev-session');
  assert.equal(requestInit?.method, 'POST');
  assert.equal(requestInit?.credentials, 'include');
  assert.equal(String(requestInit?.body), '{}');
  assert.equal(new Headers(requestInit?.headers).get('Content-Type'), 'application/json');
});

test('supports same-origin requests through the Vite development proxy', async () => {
  let requestURL = '';
  const client = new ApiClient('', async (input) => {
    requestURL = String(input);
    return ok({ user: { id: 'user-1' } });
  });

  await client.getSession();

  assert.equal(requestURL, '/api/v1/auth/session');
});

test('automatically creates one local development session after an unauthorized check', async () => {
  const requests: string[] = [];
  let developmentSessionRequests = 0;
  const client = new ApiClient('', async (input) => {
    const url = String(input);
    requests.push(url);
    if (url.endsWith('/api/v1/auth/session')) {
      return Response.json({ error: 'unauthorized' }, { status: 401 });
    }
    developmentSessionRequests += 1;
    await new Promise((resolve) => setTimeout(resolve, 5));
    return ok({ user: { id: 'dev-user', organizationId: 'dev-org', role: 'OWNER' } });
  });

  const [first, second] = await Promise.all([client.getSession(true), client.getSession(true)]);

  assert.equal(first.id, 'dev-user');
  assert.equal(second.id, 'dev-user');
  assert.equal(developmentSessionRequests, 1);
  assert.equal(requests.filter((url) => url.endsWith('/api/v1/auth/session')).length, 1);
});

test('does not auto-create a development session for non-401 errors', async () => {
  let requests = 0;
  const client = new ApiClient('', async () => {
    requests += 1;
    return Response.json({ error: 'service_unavailable' }, { status: 503 });
  });

  await assert.rejects(client.getSession(true), (error: unknown) => error instanceof ApiError && error.status === 503);
  assert.equal(requests, 1);
});

test('calls the fetcher with the global object as its receiver', async () => {
  let receiver: unknown;
  const fetcher: typeof fetch = function (this: unknown) {
    receiver = this;
    return Promise.resolve(ok({ user: { id: 'user-1' } }));
  };
  const client = new ApiClient('', fetcher);

  await client.getSession();

  assert.equal(receiver, globalThis);
});
