import { ApiClient, ApiError, type EventPage, type ProjectEvent } from './api';

const eventTypes = new Set([
  'task.created', 'task.updated', 'task.state_changed', 'task.instruction_added',
  'employee.state_changed', 'execution_attempt.state_changed', 'workspace.state_changed',
  'session.started', 'session.paused', 'session.resumed', 'session.completed', 'session.failed', 'session.canceled',
  'agent.message', 'file.activity', 'command.started', 'command.output', 'command.completed', 'command.failed',
  'check.started', 'check.passed', 'check.failed', 'check.overridden',
  'preview.started', 'preview.screenshot_captured', 'preview.failed',
  'git.commit_created', 'git.push_completed', 'git.push_failed', 'pull_request.created', 'pull_request.updated',
  'approval.requested', 'approval.resolved', 'artifact.created', 'audit.action_recorded',
]);
const envelopeKeys = new Set(['eventId', 'schemaVersion', 'eventType', 'organizationId', 'projectId', 'taskId', 'employeeId', 'attemptId', 'sessionId', 'workspaceId', 'streamSequence', 'occurredAt', 'recordedAt', 'producer', 'actor', 'correlationId', 'causationId', 'data']);
const uuid = (value: unknown): value is string => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const timestamp = (value: unknown) => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(value) && !Number.isNaN(Date.parse(value));
const decimalCursor = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value) && value.length <= 19 && BigInt(value) <= 9223372036854775807n;
// Match the backend's 2 MiB payload guard with bounded envelope overhead.
// Go JSON escaping can expand a legal task.created payload beyond one MiB.
const maxEventFrameCharacters = 2 * 1024 * 1024 + 16 * 1024;

function checkedEvent(value: unknown, scope: { organizationId: string; projectId: string }): ProjectEvent {
  if (!object(value) || Object.keys(value).length !== envelopeKeys.size || Object.keys(value).some((key) => !envelopeKeys.has(key)) ||
    value.schemaVersion !== '1.0.0' || typeof value.eventType !== 'string' || !eventTypes.has(value.eventType) ||
    value.organizationId !== scope.organizationId || value.projectId !== scope.projectId || !uuid(value.eventId) || !uuid(value.correlationId) ||
    !Number.isSafeInteger(value.streamSequence) || (value.streamSequence as number) < 1 || !timestamp(value.occurredAt) || !timestamp(value.recordedAt) ||
    !['api', 'workflow', 'gateway', 'worker', 'runtime_adapter', 'github_webhook', 'reconciler'].includes(String(value.producer)) ||
    !object(value.actor) || Object.keys(value.actor).length !== 2 || !['owner', 'employee', 'system', 'integration'].includes(String(value.actor.type)) ||
    typeof value.actor.id !== 'string' || value.actor.id.length < 1 || value.actor.id.length > 128 || !object(value.data) ||
    ['taskId', 'attemptId', 'sessionId', 'workspaceId', 'causationId'].some((key) => value[key] !== null && !uuid(value[key])) ||
    (value.employeeId !== null && !uuid(value.employeeId))) throw new Error('invalid_event');
  return value as unknown as ProjectEvent;
}

function checkedPage(value: EventPage, scope: { organizationId: string; projectId: string }): EventPage {
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 100 || !decimalCursor(value.nextCursor) || typeof value.hasMore !== 'boolean') throw new Error('invalid_event_page');
  const items = value.items.map((event) => checkedEvent(event, scope));
  const seen = new Set<string>();
  let previous = 0;
  for (const event of items) {
    if (event.streamSequence <= previous || seen.has(event.eventId) || BigInt(event.streamSequence) > BigInt(value.nextCursor)) throw new Error('invalid_event_page');
    previous = event.streamSequence;
    seen.add(event.eventId);
  }
  return { ...value, items };
}

export type EventConnection = 'loading' | 'connecting' | 'live' | 'reconnecting' | 'error' | 'signed-out';
export interface ProjectActivity {
  projectId: string;
  items: ProjectEvent[];
  status: EventConnection;
  historyReset: boolean;
  truncated: boolean;
  error: string | null;
}
export interface EventTransport {
  onopen: ((event: Event) => void) | null;
  onerror: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent<string>) => void) | null;
  close(): void;
}
export type EventTransportFactory = (url: string, options: { withCredentials: boolean }) => EventTransport;
export interface EventCallbacks {
  onChange: (state: ProjectActivity) => void;
  onFact?: (event: ProjectEvent) => void;
  onResync?: () => void;
  onUnauthorized?: () => void;
}

export function observeProjectEvents(
  client: ApiClient,
  scope: { organizationId: string; projectId: string },
  callbacks: EventCallbacks,
  createTransport: EventTransportFactory = (url, options) => new EventSource(url, options),
) {
  let active = true;
  let cursor = '0';
  let source: EventTransport | null = null;
  let probing = false;
  let resyncNeeded = false;
  const abort = new AbortController();
  let state: ProjectActivity = { projectId: scope.projectId, items: [], status: 'loading', historyReset: false, truncated: false, error: null };
  const publish = (update: Partial<ProjectActivity>) => {
    if (!active) return;
    state = { ...state, ...update };
    callbacks.onChange(state);
  };
  publish({});

  const fail = (error: unknown) => {
    if (!active) return;
    source?.close();
    source = null;
    if (error instanceof ApiError && error.status === 401) {
      publish({ items: [], status: 'signed-out', error: 'Sesi berakhir. Masuk kembali.' });
      callbacks.onUnauthorized?.();
    } else if (error instanceof ApiError && [403, 404].includes(error.status)) {
      publish({ items: [], status: 'error', error: 'Activity tidak tersedia untuk project atau akses ini.' });
    } else {
      publish({ status: 'error', error: 'Activity belum bisa dimuat. Coba hubungkan ulang.' });
    }
  };

  const loadSnapshot = async (historyReset = false) => {
    const page = checkedPage(await client.listEvents(scope.projectId, undefined, abort.signal), scope);
    if (!active) return;
    cursor = page.nextCursor;
    publish({ items: page.items, status: 'connecting', historyReset, truncated: (page.items[0]?.streamSequence ?? 1) > 1, error: null });
    attachStream();
  };

  const attachStream = () => {
    const stream = createTransport(client.eventStreamURL(scope.projectId, cursor), { withCredentials: true });
    source = stream;
    const current = () => active && source === stream;
    stream.onopen = () => {
      if (!current()) return;
      publish({ status: 'live', error: null });
      if (resyncNeeded) {
        resyncNeeded = false;
        callbacks.onResync?.();
      }
    };
    stream.onmessage = (message) => {
      if (!current()) return;
      let event: ProjectEvent;
      try {
        if (message.data.length > maxEventFrameCharacters) throw new Error('event_too_large');
        event = checkedEvent(JSON.parse(message.data), scope);
        if (message.lastEventId !== String(event.streamSequence)) throw new Error('event_cursor_mismatch');
      } catch {
        stream.close();
        source = null;
        publish({ status: 'error', error: 'Event tidak valid. Muat ulang Activity untuk mengambil snapshot aman.' });
        return;
      }
      if (BigInt(event.streamSequence) <= BigInt(cursor) || state.items.some((item) => item.eventId === event.eventId)) return;
      cursor = String(event.streamSequence);
      const items = [...state.items, event];
      publish({ items: items.slice(-100), truncated: state.truncated || items.length > 100, status: 'live', error: null });
      callbacks.onFact?.(event);
    };
    stream.onerror = () => {
      if (!current() || state.status === 'error' || state.status === 'signed-out') return;
      resyncNeeded = true;
      publish({ status: 'reconnecting' });
      if (probing) return;
      probing = true;
      // Native EventSource replays with Last-Event-ID. This read diagnoses
      // auth/cursor failures that the EventSource API deliberately conceals.
      void client.listEvents(scope.projectId, cursor, abort.signal).then((page) => {
        if (current()) checkedPage(page, scope);
      }).catch(async (error: unknown) => {
        if (!current()) return;
        if (error instanceof ApiError && ['event_cursor_expired', 'event_cursor_ahead'].includes(error.code)) {
          stream.close();
          source = null;
          await loadSnapshot(true).catch(fail);
        } else if (!(error instanceof ApiError) || [401, 403, 404].includes(error.status)) {
          fail(error);
        }
      }).finally(() => { probing = false; });
    };
  };

  const ready = loadSnapshot().catch(fail);

  return {
    ready,
    close() {
      active = false;
      abort.abort();
      source?.close();
    },
  };
}
