import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import { OfficeScene, ProjectFolderActions, SaveDraftToBacklogButton, WorkstationContent } from './office-ui';
import type { Employee } from './api';
import type { ProjectActivity } from './project-events';

const deni: Employee = { id: 'employee-1', name: 'Deni', role: 'Frontend Engineer', department: 'Engineering', status: 'IDLE' };

test('offers an explicit backlog action only for a DRAFT and exposes its pending state', () => {
  const draft = renderToStaticMarkup(<SaveDraftToBacklogButton status="DRAFT" pending={false} onSave={() => {}} />);
  assert.match(draft, /Simpan ke backlog/);
  assert.doesNotMatch(draft, /disabled=""/);

  const pending = renderToStaticMarkup(<SaveDraftToBacklogButton status="DRAFT" pending onSave={() => {}} />);
  assert.match(pending, /Menyimpan ke backlog/);
  assert.match(pending, /disabled=""/);

  const backlog = renderToStaticMarkup(<SaveDraftToBacklogButton status="BACKLOG" pending={false} onSave={() => {}} />);
  assert.equal(backlog, '');
});

test('shows allowlisted local folder actions, pending state, and an unavailable state', () => {
  const available = renderToStaticMarkup(<ProjectFolderActions status="available" pendingEditor={null} onOpen={() => {}} onRetry={() => {}} />);
  assert.match(available, /aria-label="Buka folder project di Explorer"/);
  assert.match(available, /aria-label="Buka folder project di VS Code"/);
  assert.match(available, /role="group"/);

  const pending = renderToStaticMarkup(<ProjectFolderActions status="available" pendingEditor="vscode" onOpen={() => {}} onRetry={() => {}} />);
  assert.match(pending, /Membuka VS Code/);
  assert.match(pending, /disabled=""/);

  const unavailable = renderToStaticMarkup(<ProjectFolderActions status="unavailable" pendingEditor={null} onOpen={() => {}} onRetry={() => {}} />);
  assert.match(unavailable, /Folder lokal belum dikonfigurasi/);
  assert.match(unavailable, /disabled=""/);
  assert.match(unavailable, /Cek ulang folder/);

  const bridgeError = renderToStaticMarkup(<ProjectFolderActions status="error" pendingEditor={null} onOpen={() => {}} onRetry={() => {}} />);
  assert.match(bridgeError, /Local folder bridge tidak terhubung/);
  assert.match(bridgeError, /Cek ulang folder/);
});

test('workstation Activity renders persisted facts, scoped to the employee, without exposing raw event payloads', () => {
  const activity: ProjectActivity = {
    projectId: 'project-1', status: 'live', error: null, historyReset: false, truncated: false,
    items: [{
      eventId: 'event-1', schemaVersion: '1.0.0', eventType: 'task.created', organizationId: 'org-1', projectId: 'project-1',
      taskId: 'task-1', employeeId: deni.id, attemptId: null, sessionId: null, workspaceId: null, streamSequence: 7,
      occurredAt: '2026-10-06T00:00:00Z', recordedAt: '2026-10-06T00:00:00Z', producer: 'api',
      actor: { type: 'owner', id: 'user-1' }, correlationId: 'correlation-1', causationId: null,
      data: { title: 'Instruksi tersimpan', initialState: 'DRAFT', description: 'raw-private-instruction-not-for-timeline', objectKey: 'never-show-private-storage-key' },
    }],
  };
  const markup = renderToStaticMarkup(<WorkstationContent employee={deni} tasks={[]} activity={activity} onCompose={() => {}} />);
  assert.match(markup, /PERSISTED PROJECT EVENTS/);
  assert.match(markup, /SSE terhubung/);
  assert.match(markup, /Instruksi tersimpan/);
  assert.match(markup, /task.created/);
  assert.match(markup, /data-sequence="7"/);
  assert.doesNotMatch(markup, /raw-private-instruction-not-for-timeline|never-show-private-storage-key|BUKAN RUNTIME EVENTS/);
  const otherEmployeeMarkup = renderToStaticMarkup(<WorkstationContent employee={{ ...deni, id: 'other-employee' }} tasks={[]} activity={activity} onCompose={() => {}} />);
  assert.doesNotMatch(otherEmployeeMarkup, /Instruksi tersimpan/);
});

test('failed Activity is distinct from empty history and exposes an accessible retry', () => {
  const activity: ProjectActivity = { projectId: 'project-1', items: [], status: 'error', error: 'Activity belum bisa dimuat.', historyReset: false, truncated: false };
  const markup = renderToStaticMarkup(<WorkstationContent employee={deni} tasks={[]} activity={activity} onRetryActivity={() => {}} onCompose={() => {}} />);
  assert.match(markup, /Activity belum bisa dimuat/);
  assert.match(markup, /Hubungkan ulang Activity/);
  assert.doesNotMatch(markup, /Belum ada event tersimpan/);
});

test('failed employee refresh labels the cached snapshot while Activity stays available', () => {
  const activity: ProjectActivity = { projectId: 'project-1', items: [], status: 'reconnecting', error: null, historyReset: false, truncated: false };
  const markup = renderToStaticMarkup(<WorkstationContent employee={deni} tasks={[]} activity={activity} employeeSnapshotError="Cannot reach the API." onRetryEmployee={() => {}} onCompose={() => {}} />);
  assert.match(markup, /employee-refresh-warning/);
  assert.match(markup, /Status terakhir yang berhasil dimuat/);
  assert.match(markup, /belum dikonfirmasi ulang/);
  assert.match(markup, /Cannot reach the API/);
  assert.match(markup, /Refresh employee/);
  assert.match(markup, /data-connection="reconnecting"/);
  assert.match(markup, /Deni/);
  assert.match(markup, /IDLE/);
  assert.doesNotMatch(markup, /Workspace belum bisa dimuat/);
});

test('office projects persisted employee identity and status instead of simulated work', () => {
  const markup = renderToStaticMarkup(<OfficeScene employee={deni} loading={false} onInspect={() => {}} />);
  assert.match(markup, /Deni/);
  assert.match(markup, /IDLE/);
  assert.match(markup, /Frontend Engineer/);
  assert.match(markup, /Buka workstation Deni/);
  assert.doesNotMatch(markup, /CODING|Tests passed|Nabil|Raka/);
});

test('office is a side-view world with character and computer interactions, not a dashboard illustration', () => {
  const markup = renderToStaticMarkup(<OfficeScene employee={deni} loading={false} onInspect={() => {}} />);
  assert.match(markup, /data-world="side-view"/);
  assert.match(markup, /studio-world\.png/);
  assert.match(markup, /Buka profil Deni/);
  assert.match(markup, /Buka workstation Deni/);
  assert.doesNotMatch(markup, /office-backdrop|A LITTLE PLACE FOR BIG IDEAS|DESK AVAILABLE|FLOOR 01/);
});

test('studio world renders three explorable areas, jump control and full Owner identity', () => {
  const ownerName = 'Local Development Owner with Long Name';
  const markup = renderToStaticMarkup(<OfficeScene employee={deni} ownerName={ownerName} loading={false} onInspect={() => {}} />);
  assert.match(markup, /data-world-width="1920"/);
  assert.match(markup, /data-area="main-office"/);
  assert.match(markup, /data-area="garden"/);
  assert.match(markup, /data-area="workshop"/);
  assert.match(markup, /studio-world-garden\.png/);
  assert.match(markup, /studio-world-workshop\.png/);
  assert.match(markup, /world-platform/);
  assert.match(markup, /world-obstacle/);
  assert.match(markup, /world-jump/);
  assert.match(markup, new RegExp(ownerName));
  assert.match(markup, /jelajahi|eksplorasi/i);
});

test('workstation exposes the unavailable runtime honestly and cannot start an agent', () => {
  const markup = renderToStaticMarkup(<WorkstationContent employee={deni} tasks={[]} onCompose={() => {}} />);
  assert.match(markup, /Deni/);
  assert.match(markup, /IDLE/);
  assert.match(markup, /Belum ada sesi runtime/);
  assert.match(markup, /Start[^<]*<\/button>|Start/);
  assert.match(markup, /disabled=""/);
  assert.doesNotMatch(markup, /Tests passed|Coding now|fake-terminal/);
});
