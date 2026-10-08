import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import type { Employee } from './api';

const deni: Employee = { id: 'employee-1', name: 'Deni', role: 'Frontend Engineer', department: 'Engineering', status: 'IDLE' };

test('game HUD exposes real employee status and task navigation without fictional game metrics', async () => {
  const module = await import('./studio-hud').catch(() => null);
  assert.ok(module, 'The game-first HUD is not implemented yet.');
  const markup = renderToStaticMarkup(<module.StudioHUD ownerName="Owner test fixture" employee={deni} taskCount={3} employeeCount={1} activePane="office" connection="live" instruction="" onInstructionChange={() => {}} onNavigate={() => {}} canCompose={true} />);
  assert.match(markup, /class="studio-hud"/);
  assert.match(markup, /aria-label="Workspace navigation"/);
  assert.match(markup, /Task board/);
  assert.match(markup, /Beri instruksi/);
  assert.match(markup, /Owner test fixture/);
  assert.match(markup, /Deni/);
  assert.match(markup, /IDLE/);
  assert.match(markup, /3 task tersimpan/);
  assert.match(markup, /SSE terhubung/);
  assert.doesNotMatch(markup, /\b(?:HP|MP|LEVEL|XP)\b|app-sidebar|You set the pace/);
});
