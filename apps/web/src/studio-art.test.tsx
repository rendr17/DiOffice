import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderToStaticMarkup } from 'react-dom/server';
import { PixelSprite } from './office-ui';

test('employee portraits use the same original chibi as the world at an integer pixel scale', () => {
  const markup = renderToStaticMarkup(<PixelSprite scale={2} />);
  assert.match(markup, /studio-engineer-idle\.png/);
  assert.match(markup, /width:80px;height:112px/);
  assert.doesNotMatch(markup, /2dpig-office\.png/);
});
