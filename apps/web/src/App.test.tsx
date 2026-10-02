import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import { App } from './App';

test('shows the real project foundation state without simulated task activity', () => {
  const markup = renderToStaticMarkup(<App />);

  assert.match(markup, /DiOffice/);
  assert.match(markup, /Project foundation/);
  assert.match(markup, /No task activity is running/);
});
