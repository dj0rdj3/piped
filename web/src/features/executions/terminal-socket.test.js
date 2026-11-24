import assert from 'node:assert/strict';
import test from 'node:test';
import { createTerminalSocket } from './terminal-socket.js';

function harness(onOutput) {
  const sockets = [];
  class FakeSocket {
    static OPEN = 1;
    constructor(url) {
      this.url = url;
      this.readyState = 0;
      this.sent = [];
      sockets.push(this);
    }
    send(data) {
      this.sent.push(JSON.parse(data));
    }
    close(code = 1000) {
      if (this.readyState === 3) {
        return;
      }
      this.readyState = 3;
      this.onclose({ code });
    }
    open() {
      this.readyState = 1;
      this.onopen();
    }
    message(message) {
      return this.onmessage({ data: JSON.stringify(message) });
    }
  }
  const pendingTimers = new Map();
  let timerID = 0;
  const events = [];
  const stream = createTerminalSocket({
    url: 'ws://127.0.0.1/api/runs/public/hosts/public/terminal?after=41',
    WebSocket: FakeSocket,
    timers: {
      setTimeout(callback) {
        pendingTimers.set(++timerID, callback);
        return timerID;
      },
      clearTimeout(id) {
        pendingTimers.delete(id);
      },
    },
    onOpen: () => events.push('open'),
    onOutput: onOutput || ((chunks) => events.push(chunks)),
    onState: (state) => events.push(state),
    onComplete: () => events.push('complete'),
    onClose: () => events.push('close'),
    onAuthError: () => events.push('auth'),
  });
  return {
    control: sockets[0],
    output: sockets[1],
    stream,
    events,
    pendingTimers,
    open() {
      sockets[0].open();
      sockets[1].open();
    },
    async finish() {
      await sockets[1].message({ type: 'complete' });
      await sockets[0].message({ type: 'state', run: { finished_at: 1 }, host_keys: [] });
    },
  };
}

test('terminal control and output have independent connections and acknowledgements', async () => {
  const h = harness();
  h.open();
  assert.equal(h.stream.connected, true);
  assert.equal(new URL(h.control.url).searchParams.get('channel'), 'control');
  assert.equal(new URL(h.output.url).searchParams.get('channel'), 'output');
  assert.equal(new URL(h.output.url).searchParams.get('after'), '41');
  const chunks = [{ id: 42, data: 'aGk=' }];
  await h.output.message({ type: 'output', chunks });
  assert.deepEqual(h.output.sent, [{ type: 'output_ack', id: 1, after: 42 }]);
  const input = h.stream.sendInput('hello\n');
  assert.deepEqual(h.control.sent, [{ type: 'input', id: 2, data: 'hello\n' }]);
  await h.output.message({ type: 'input_result', id: 2 });
  assert.equal(h.pendingTimers.size, 1);
  await h.control.message({ type: 'input_result', id: 2 });
  await input;
  assert.equal(h.pendingTimers.size, 0);
  h.stream.close();
  assert.equal(h.control.readyState, 3);
  assert.equal(h.output.readyState, 3);
});

test('slow output consumption cannot delay status updates or Ctrl+C acknowledgement', async () => {
  let release;
  const h = harness(
    () =>
      new Promise((resolve) => {
        release = resolve;
      }),
  );
  h.open();
  const consume = h.output.message({ type: 'output', chunks: [{ id: 42, data: 'eA==' }] });
  assert.equal(h.output.sent.length, 0);
  const input = h.stream.sendInput('\u0003');
  await h.control.message({ type: 'input_result', id: 1 });
  await input;
  const state = { type: 'state', run: { status: 'interrupted', finished_at: 1 }, host_keys: [] };
  await h.control.message(state);
  assert.deepEqual(h.events, ['open', state]);
  release();
  await consume;
  assert.deepEqual(h.output.sent, [{ type: 'output_ack', id: 2, after: 42 }]);
  h.stream.close();
});

test('disconnect rejects uncertain input, closes both channels, and never resends', async () => {
  const h = harness();
  h.open();
  const input = h.stream.sendInput('perform-once\n');
  h.output.close(1006);
  await assert.rejects(input, /not confirmed/);
  assert.equal(h.pendingTimers.size, 0);
  assert.equal(h.control.sent.length, 1);
  assert.equal(h.control.readyState, 3);
  assert.deepEqual(h.events, ['open', 'close']);
});

test('input timeouts, rejection, and byte bounds preserve explicit failure without retries', async () => {
  const h = harness();
  h.open();
  await assert.rejects(h.stream.sendInput(''), /1-4096/);
  await assert.rejects(h.stream.sendInput('\u20ac'.repeat(1400)), /1-4096/);
  assert.equal(h.control.sent.length, 0);
  const input = h.stream.sendInput('first\n');
  [...h.pendingTimers.values()][0]();
  await assert.rejects(input, /not confirmed/);
  const next = h.stream.sendInput('second\n');
  await h.control.message({
    type: 'input_result',
    id: 2,
    error: 'Execution no longer accepts input.',
  });
  await assert.rejects(next, /no longer accepts/);
  h.stream.close();
});

test('authorization closure on either channel signs out and selection changes suppress stale messages', async () => {
  for (const name of ['control', 'output']) {
    const h = harness();
    h.open();
    h[name].close(4001);
    assert.deepEqual(h.events, ['open', 'auth']);
    assert.equal(h.stream.connected, false);
  }
  const changed = harness();
  changed.stream.close();
  changed.open();
  await changed.control.message({ type: 'state', run: {}, host_keys: [] });
  await changed.output.message({ type: 'output', chunks: [] });
  assert.deepEqual(changed.events, []);
  await assert.rejects(changed.stream.sendInput('stale'), /not ready/);
});

test('completion waits for final status, final output, and an in-flight input acknowledgement', async () => {
  const h = harness();
  h.open();
  const input = h.stream.sendInput('\u0003');
  await h.output.message({ type: 'complete' });
  assert.deepEqual(h.events, ['open']);
  const state = { type: 'state', run: { finished_at: 1 }, host_keys: [] };
  await h.control.message(state);
  assert.deepEqual(h.events, ['open', state]);
  await h.control.message({ type: 'input_result', id: 1 });
  await input;
  assert.deepEqual(h.events, ['open', state, 'complete']);
  await h.output.message({ type: 'complete' });
  assert.equal(h.events.filter((event) => event === 'complete').length, 1);
  h.stream.close();
});

test('completed terminals load history over output channel and reject mismatched responses', async () => {
  const h = harness();
  h.open();
  await h.finish();
  assert.equal(h.stream.connected, true);
  await assert.rejects(h.stream.sendInput('no completed input'), /not ready/);
  const history = h.stream.loadHistory(40, 123);
  assert.deepEqual(h.output.sent, [{ type: 'history', id: 1, before: 40, offset: 123 }]);
  await h.control.message({ type: 'history', id: 1 });
  await h.output.message({ type: 'input_result', id: 1 });
  assert.equal(h.pendingTimers.size, 1);
  const response = { type: 'history', id: 1, chunks: [{ id: 39, data: 'aGk=' }], has_more: true };
  await h.output.message(response);
  assert.deepEqual(await history, response);
  h.stream.close();
});

test('history disconnect after completion reconnects without input retry warnings', async () => {
  const h = harness();
  h.open();
  await h.finish();
  const history = h.stream.loadHistory(40);
  h.output.close(1006);
  await assert.rejects(history, /History connection closed/);
  assert.equal(h.events.at(-1), 'close');
  assert.equal(h.pendingTimers.size, 0);
});
