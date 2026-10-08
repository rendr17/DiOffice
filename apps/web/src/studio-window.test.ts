import { test } from 'node:test';
import assert from 'node:assert/strict';

test('game window pointer drag keeps every edge inside the viewport', async () => {
  const navigation = await import('./studio-navigation');
  assert.equal(typeof navigation.clampWindowDrag, 'function', 'Bounded native game-window drag is missing.');
  const rect = { left: 50, top: 40, width: 200, height: 100 };
  const viewport = { width: 390, height: 844 };
  for (const delta of [{ x: -1000, y: -1000 }, { x: 1000, y: 1000 }, { x: 0, y: 0 }]) {
    const offset = navigation.clampWindowDrag(rect, delta, viewport);
    assert.ok(rect.left + offset.x >= 8);
    assert.ok(rect.top + offset.y >= 8);
    assert.ok(rect.left + rect.width + offset.x <= viewport.width - 8);
    assert.ok(rect.top + rect.height + offset.y <= viewport.height - 8);
  }
});
