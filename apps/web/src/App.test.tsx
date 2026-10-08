import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import { App } from './App';

test('shows an honest session-check state without simulated task activity', () => {
  const markup = renderToStaticMarkup(<App />);

  assert.match(markup, /DiOffice/);
  assert.match(markup, /Checking your session/);
  assert.doesNotMatch(markup, /simulated progress/i);
});

test('uses the pixel-office shell while checking the real session', () => {
  const markup = renderToStaticMarkup(<App />);

  assert.match(markup, /pixel-app/);
  assert.match(markup, /Your little software studio/);
  assert.doesNotMatch(markup, /Coding|Tests passed|Agent running/);
});
