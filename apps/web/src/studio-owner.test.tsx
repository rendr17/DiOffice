import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import { OfficeScene } from './office-ui';
import type { Employee } from './api';

const deni: Employee = { id: 'employee-1', name: 'Deni', role: 'Frontend Engineer', department: 'Engineering', status: 'IDLE' };

test('the human Owner has a separate locally controlled avatar, never simulated employee work', () => {
  const markup = renderToStaticMarkup(<OfficeScene employee={deni} ownerName="Owner fixture" loading={false} onInspect={() => {}} />);
  assert.match(markup, /world-owner/);
  assert.match(markup, /data-animation="idle"/);
  assert.match(markup, /studio-owner-states\.png/);
  assert.match(markup, /studio-owner-walk\.png/);
  assert.match(markup, /Gerakkan avatar Owner fixture/);
  assert.match(markup, /panah kiri\/kanan/);
  assert.match(markup, /IDLE/);
  assert.doesNotMatch(markup, /CODING|Tests passed|LEVEL|HP/);
});
