import assert from 'node:assert/strict';
import test from 'node:test';
import { readExecutionView, writeExecutionView } from './execution-view.js';

function storage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}
test('execution selections survive revisits and are isolated by account', () => {
  const browser = storage();
  const view = { jobID: 'a'.repeat(32), runID: 'b'.repeat(32), hostID: 'c'.repeat(32) };
  writeExecutionView({ localStorage: browser }, 1, {
    ...view,
    input: 'private draft',
    output: 'private output',
  });
  assert.deepEqual(readExecutionView({ localStorage: browser }, 1), view);
  assert.equal(readExecutionView({ localStorage: browser }, 2), null);
  assert.doesNotMatch(browser.getItem('piped.execution-view.1'), /private|input|output/);
  writeExecutionView({ localStorage: browser }, 1, null);
  assert.equal(readExecutionView({ localStorage: browser }, 1), null);
});
test('corrupt selections and unavailable browser storage do not break jobs', () => {
  const browser = storage();
  browser.setItem('piped.execution-view.1', '{');
  assert.equal(readExecutionView({ localStorage: browser }, 1), null);
  browser.setItem('piped.execution-view.1', JSON.stringify({ jobID: '../users' }));
  assert.equal(readExecutionView({ localStorage: browser }, 1), null);
  const blocked = {
    getItem() {
      throw new Error('blocked');
    },
    setItem() {
      throw new Error('blocked');
    },
    removeItem() {
      throw new Error('blocked');
    },
  };
  assert.equal(readExecutionView({ localStorage: blocked }, 1), null);
  const unavailable = {
    get localStorage() {
      throw new Error('blocked');
    },
  };
  assert.equal(readExecutionView(unavailable, 1), null);
  assert.doesNotThrow(() => writeExecutionView(unavailable, 1, null));
  assert.doesNotThrow(() =>
    writeExecutionView({ localStorage: blocked }, 1, { jobID: 'a'.repeat(32) }),
  );
  assert.doesNotThrow(() => writeExecutionView({ localStorage: blocked }, 1, null));
});
