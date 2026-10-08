import assert from 'node:assert/strict';
import test from 'node:test';
import { commandToTaskFields } from './task-command';

test('maps a chat-style instruction into the existing task contract', () => {
  const instruction = '\nUbah desain halaman task desk\nIkuti referensi visual yang saya lampirkan dan pertahankan layout responsif.\n';

  assert.deepEqual(commandToTaskFields(instruction), {
    title: 'Ubah desain halaman task desk',
    description: 'Ubah desain halaman task desk\nIkuti referensi visual yang saya lampirkan dan pertahankan layout responsif.',
    acceptanceCriteria: [],
  });
});

test('rejects a blank instruction', () => {
  assert.throws(() => commandToTaskFields(' \n\t '), /instruction is required/i);
});

test('limits a derived title without splitting Unicode characters', () => {
  const instruction = `${'🧭'.repeat(205)}\nKeep the full command in the description.`;
  const fields = commandToTaskFields(instruction);

  assert.equal(Array.from(fields.title).length, 200);
  assert.equal(fields.title.endsWith('…'), true);
  assert.equal(fields.description, instruction);
});
