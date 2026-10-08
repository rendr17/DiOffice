import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ApiClient, type ProjectEvent } from './api';
import { observeProjectEvents, type ProjectActivity, type EventTransport } from './project-events';

const scope = { organizationId: '00000000-0000-4000-8000-000000000001', projectId: '00000000-0000-4000-8000-000000000002' };
const employeeId = '00000000-0000-4000-8000-000000000003';
function fact(sequence: number): ProjectEvent {
  return {
    eventId: `00000000-0000-4000-8000-${String(sequence).padStart(12, '0')}`,
    schemaVersion: '1.0.0', eventType: 'task.created', ...scope,
    taskId: '00000000-0000-4000-8000-000000000004', employeeId,
    attemptId: null, sessionId: null, workspaceId: null, streamSequence: sequence,
    occurredAt: '2026-10-06T00:00:00Z', recordedAt: '2026-10-06T00:00:00Z',
    producer: 'api', actor: { type: 'owner', id: '00000000-0000-4000-8000-000000000005' },
    correlationId: '00000000-0000-4000-8000-000000000006', causationId: null,
    data: { title: 'Persisted draft', description: 'Read-only fixture', assigneeEmployeeId: employeeId, priority: 'NORMAL', initialState: 'DRAFT', acceptanceCriteria: [] },
  };
}
class Transport implements EventTransport {
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  closed = false;
  close() { this.closed = true; }
  open() { this.onopen?.(new Event('open')); }
  fail() { this.onerror?.(new Event('error')); }
  emit(event: ProjectEvent) { this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(event), lastEventId: String(event.streamSequence) })); }
}
const tick = () => new Promise<void>((resolve) => setImmediate(resolve));

test('rejects malformed, unknown-version and cross-scope facts without advancing the visible timeline', async () => {
  for (const changed of [
    { organizationId: '00000000-0000-4000-8000-000000000099' },
    { projectId: '00000000-0000-4000-8000-000000000099' },
    { eventType: 'agent.started' }, { schemaVersion: '2.0.0' },
    { streamSequence: Number.MAX_SAFE_INTEGER + 1 }, { recordedAt: 'not-a-date' },
    { eventId: 'not-a-uuid' }, { data: null }, { objectKey: 'private-storage-key' },
  ]) {
    let state: ProjectActivity | undefined;
    const transport = new Transport();
    const client = new ApiClient('', async () => Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
    const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; } }, () => transport);
    await subscription.ready;
    transport.emit({ ...fact(2), ...changed } as ProjectEvent);
    assert.equal(state?.status, 'error');
    assert.deepEqual(state?.items.map((event) => event.streamSequence), [1]);
    assert.equal(transport.closed, true);
    subscription.close();
  }
});

test('does not subscribe using a malformed or cross-project snapshot', async () => {
  let subscribed = false;
  let state: ProjectActivity | undefined;
  const client = new ApiClient('', async () => Response.json({ items: [{ ...fact(1), projectId: 'other-project' }], nextCursor: '1', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; } }, () => {
    subscribed = true;
    return new Transport();
  });
  await subscription.ready;
  assert.equal(subscribed, false);
  assert.equal(state?.status, 'error');
  assert.deepEqual(state?.items, []);
  subscription.close();
});

test('does not accept late facts after a protocol failure closes the transport', async () => {
  let state: ProjectActivity | undefined;
  const transport = new Transport();
  const client = new ApiClient('', async () => Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; } }, () => transport);
  await subscription.ready;
  transport.emit({ ...fact(2), schemaVersion: 'unsupported' });
  transport.emit(fact(2));
  assert.equal(state?.status, 'error');
  assert.equal(state?.items.length, 1);
  subscription.close();
});

test('accepts maximum contract task criteria after Go JSON HTML escaping', async () => {
  let state: ProjectActivity | undefined;
  const delivered: ProjectEvent[] = [];
  const transport = new Transport();
  const client = new ApiClient('', async () => Response.json({ items: [], nextCursor: '0', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, {
    onChange: (value) => { state = value; }, onFact: (event) => delivered.push(event),
  }, () => transport);
  await subscription.ready;
  const event = { ...fact(1), data: { ...fact(1).data, description: '<'.repeat(20000), acceptanceCriteria: Array.from({ length: 100 }, () => '<'.repeat(2000)) } };
  // encoding/json escapes '<' on the real SSE response, unlike JSON.stringify.
  const data = JSON.stringify(event).replaceAll('<', '\\u003c');
  assert.ok(data.length > 256 * 1024);
  transport.onmessage?.(new MessageEvent('message', { data, lastEventId: '1' }));
  assert.equal(state?.status, 'live');
  assert.deepEqual(state?.items, [event]);
  assert.deepEqual(delivered, [event]);
  assert.equal(transport.closed, false);
  subscription.close();
});

test('rejects oversized frames before parsing or appending facts', async () => {
  let state: ProjectActivity | undefined;
  const delivered: ProjectEvent[] = [];
  const transport = new Transport();
  const client = new ApiClient('', async () => Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, {
    onChange: (value) => { state = value; }, onFact: (event) => delivered.push(event),
  }, () => transport);
  await subscription.ready;
  transport.onmessage?.(new MessageEvent('message', { data: ' '.repeat(3 * 1024 * 1024), lastEventId: '2' }));
  assert.equal(state?.status, 'error');
  assert.deepEqual(state?.items.map((event) => event.streamSequence), [1]);
  assert.deepEqual(delivered, []);
  assert.equal(transport.closed, true);
  subscription.close();
});

test('keeps a bounded recent timeline while preserving the replay cursor', async () => {
  let state: ProjectActivity | undefined;
  const transport = new Transport();
  const client = new ApiClient('', async () => Response.json({ items: [], nextCursor: '0', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; } }, () => transport);
  await subscription.ready;
  for (let index = 1; index <= 120; index += 1) transport.emit(fact(index));
  assert.equal(state?.items.length, 100);
  assert.equal(state?.items[0].streamSequence, 21);
  assert.equal(state?.items.at(-1)?.streamSequence, 120);
  assert.equal(state?.truncated, true);
  subscription.close();
});

test('ignores late history and transport callbacks after scope disposal', async () => {
  let finish: (value: Response) => void = () => {};
  const states: ProjectActivity[] = [];
  let subscribed = false;
  const client = new ApiClient('', () => new Promise<Response>((resolve) => { finish = resolve; }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => states.push(value) }, () => {
    subscribed = true;
    return new Transport();
  });
  subscription.close();
  finish(Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
  await subscription.ready;
  assert.equal(subscribed, false);
  assert.equal(states.length, 1);
});

test('retains facts during native reconnect and requests an authoritative snapshot on reopen', async () => {
  let state: ProjectActivity | undefined;
  let resyncs = 0;
  const transport = new Transport();
  const client = new ApiClient('', async () => Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; }, onResync: () => { resyncs += 1; } }, () => transport);
  await subscription.ready;
  transport.open();
  transport.fail();
  await tick();
  assert.equal(state?.status, 'reconnecting');
  assert.equal(state?.items.length, 1);
  assert.equal(transport.closed, false);
  transport.open();
  assert.equal(state?.status, 'live');
  assert.equal(resyncs, 1);
  subscription.close();
});

test('closes the stream and removes tenant facts when the session is revoked', async () => {
  let state: ProjectActivity | undefined;
  let revoked = 0;
  let reads = 0;
  const transport = new Transport();
  const client = new ApiClient('', async () => ++reads === 1
    ? Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false })
    : Response.json({ error: 'unauthorized' }, { status: 401 }));
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; }, onUnauthorized: () => { revoked += 1; } }, () => transport);
  await subscription.ready;
  transport.fail();
  await tick();
  assert.equal(transport.closed, true);
  assert.equal(state?.status, 'signed-out');
  assert.deepEqual(state?.items, []);
  assert.equal(revoked, 1);
  subscription.close();
});

test('replaces an expired cursor with an explicit authorized snapshot instead of silently skipping history', async () => {
  let state: ProjectActivity | undefined;
  const urls: string[] = [];
  const transports: Transport[] = [];
  let reads = 0;
  const client = new ApiClient('', async () => {
    reads += 1;
    if (reads === 2) return Response.json({ error: 'event_cursor_expired' }, { status: 410 });
    return Response.json({ items: [fact(reads === 1 ? 1 : 9)], nextCursor: reads === 1 ? '1' : '9', hasMore: false });
  });
  const subscription = observeProjectEvents(client, scope, { onChange: (value) => { state = value; } }, (url) => {
    urls.push(url);
    const transport = new Transport();
    transports.push(transport);
    return transport;
  });
  await subscription.ready;
  transports[0].fail();
  await tick();
  assert.equal(transports[0].closed, true);
  assert.equal(state?.historyReset, true);
  assert.deepEqual(state?.items.map((event) => event.streamSequence), [9]);
  assert.ok(urls.at(-1)?.endsWith('after=9'));
  subscription.close();
});

test('loads durable history, then appends only real streamed facts after its project cursor', async () => {
  const states: ProjectActivity[] = [];
  const transport = new Transport();
  const urls: string[] = [];
  const delivered: ProjectEvent[] = [];
  const client = new ApiClient('', async () => Response.json({ items: [fact(1)], nextCursor: '1', hasMore: false }));
  const subscription = observeProjectEvents(client, scope, {
    onChange: (state) => states.push(state), onFact: (event) => delivered.push(event),
  }, (url, options) => {
    urls.push(url);
    assert.equal(options.withCredentials, true);
    return transport;
  });
  await subscription.ready;
  assert.deepEqual(states.at(-1)?.items.map((event) => event.streamSequence), [1]);
  assert.equal(states.at(-1)?.status, 'connecting');
  assert.deepEqual(urls, [`/api/v1/projects/${scope.projectId}/events/stream?after=1`]);
  transport.open();
  transport.emit(fact(2));
  transport.emit(fact(2));
  transport.emit(fact(1));
  assert.equal(states.at(-1)?.status, 'live');
  assert.deepEqual(states.at(-1)?.items.map((event) => event.streamSequence), [1, 2]);
  assert.deepEqual(delivered.map((event) => event.streamSequence), [2]);
  subscription.close();
  assert.equal(transport.closed, true);
});
