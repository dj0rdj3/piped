import assert from 'node:assert/strict';
import test from 'node:test';
import { createExecutionPoller } from './execution-poller.js';

function clock() {
  const pending = new Map();
  let id = 0;
  return {
    pending,
    setTimeout(callback, delay) {
      pending.set(++id, { callback, delay });
      return id;
    },
    clearTimeout(id) {
      pending.delete(id);
    },
  };
}

test('changing a selection refreshes immediately and cancels the idle history timer', async () => {
  const timers = clock();
  const selections = [];
  const poller = createExecutionPoller({
    load: async (options) => selections.push(options),
    onError: assert.fail,
    onSettled() {},
    delay: () => 3000,
    timers,
  });
  await poller.refresh();
  assert.equal(timers.pending.size, 1);
  await poller.refresh({ history: false });
  assert.deepEqual(selections, [{}, { history: false }]);
  assert.equal(timers.pending.size, 1);
  assert.equal([...timers.pending.values()][0].delay, 3000);
  poller.stop();
  assert.equal(timers.pending.size, 0);
});

test('rapid selection cancels stale requests without stale errors or duplicate polling', async () => {
  const timers = clock();
  const loads = [];
  const errors = [];
  let settled = 0;
  const poller = createExecutionPoller({
    load: (_options, signal) =>
      new Promise((resolve, reject) => loads.push({ signal, resolve, reject })),
    onError: (error) => errors.push(error),
    onSettled: () => settled++,
    delay: () => 750,
    timers,
  });
  const first = poller.refresh();
  const second = poller.refresh({ history: false });
  assert.equal(loads[0].signal.aborted, true);
  assert.equal(loads[1].signal.aborted, false);
  loads[1].resolve();
  await second;
  loads[0].reject(new Error('stale selection failed'));
  await first;
  assert.equal(settled, 1);
  assert.deepEqual(errors, []);
  assert.equal(timers.pending.size, 1);
  const closing = poller.refresh();
  poller.stop();
  assert.equal(loads[2].signal.aborted, true);
  loads[2].resolve();
  await closing;
  assert.equal(settled, 1);
  assert.equal(timers.pending.size, 0);
  await poller.refresh();
  assert.equal(loads.length, 3);
});
