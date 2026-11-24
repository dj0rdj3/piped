import assert from 'node:assert/strict';
import test from 'node:test';
import { Buffer } from 'node:buffer';
import { createOutputBuffer, readTerminalOutput } from './execution-output.js';

function chunk(id, text) {
  return { id, data: Buffer.from(text).toString('base64') };
}

test('opening history loads the visible tail in one request and continues from its last cursor', async () => {
  const buffer = createOutputBuffer();
  const paths = [];
  const request = async (path) => {
    paths.push(path);
    return paths.length === 1
      ? {
          chunks: Array.from({ length: 80 }, (_, index) => chunk(1000 + index, `line ${index}\n`)),
          truncated: true,
        }
      : { chunks: [chunk(1080, 'new live output\n')] };
  };
  const signal = new AbortController().signal;
  let updates = 0;
  assert.equal(
    await readTerminalOutput(buffer, '/output', request, signal, () => updates++),
    false,
  );
  assert.deepEqual(paths, ['/output?tail=1']);
  assert.match(buffer.text, /line 79\n$/);
  assert.equal(buffer.trimmed, true);
  assert.equal(buffer.after, 1079);
  await readTerminalOutput(buffer, '/output', request, signal, () => updates++);
  assert.equal(paths[1], '/output?after=1079');
  assert.match(buffer.text, /line 79\nnew live output\n$/);
  assert.equal(updates, 2);
  buffer.complete = true;
  await readTerminalOutput(buffer, '/output', request, signal, assert.fail);
  assert.equal(paths.length, 2);
});

test('live output drains successive cursor pages without waiting for polling timers', async () => {
  const buffer = createOutputBuffer();
  buffer.loaded = true;
  let requests = 0;
  let id = 0;
  const request = async () => {
    requests++;
    return { chunks: Array.from({ length: requests < 3 ? 16 : 1 }, () => chunk(++id, 'x')) };
  };
  const more = await readTerminalOutput(
    buffer,
    '/output',
    request,
    new AbortController().signal,
    () => {},
  );
  assert.equal(more, false);
  assert.equal(requests, 3);
  assert.equal(buffer.text, 'x'.repeat(33));
  assert.equal(buffer.after, 33);
});

test('continuous output yields after bounded work and reports that an immediate refresh is needed', async () => {
  const buffer = createOutputBuffer();
  buffer.loaded = true;
  let requests = 0;
  const request = async () => ({
    chunks: Array.from({ length: 16 }, () => chunk(++requests, 'x')),
  });
  assert.equal(
    await readTerminalOutput(buffer, '/output', request, new AbortController().signal, () => {}),
    true,
  );
  assert.equal(requests, 64);
});

test('cancelled selection cannot append a late response or advance its cursor', async () => {
  const buffer = createOutputBuffer();
  const controller = new AbortController();
  const request = async () => {
    controller.abort();
    return { chunks: [chunk(20, 'old host output')] };
  };
  await readTerminalOutput(buffer, '/output', request, controller.signal, assert.fail);
  assert.equal(buffer.text, '');
  assert.equal(buffer.after, 0);
  assert.equal(buffer.loaded, false);
});

test('cursor replay is ignored while split UTF-8 decoding continues across live packets', async () => {
  const { appendOutputChunks } = await import('./execution-output.js');
  const buffer = createOutputBuffer();
  const first = { id: 20, data: Buffer.from([0xe2, 0x82]).toString('base64') };
  appendOutputChunks(buffer, [first]);
  appendOutputChunks(buffer, [first, { id: 21, data: Buffer.from([0xac]).toString('base64') }]);
  assert.equal(buffer.text, '\u20ac');
  assert.equal(buffer.after, 21);
});

test('older pages join split UTF-8 and escapes without changing the live cursor', async () => {
  const { expandOutputHistory, historyCursor, prependOutputPage } =
    await import('./execution-output.js');
  const buffer = createOutputBuffer();
  const original = Buffer.from('start \u20ac\u001b[32mGREEN\u001b[0m\n');
  const request = async () => ({
    chunks: [{ id: 2, offset: 8, data: original.subarray(8).toString('base64') }],
    truncated: true,
  });
  await readTerminalOutput(buffer, '/output', request, new AbortController().signal, () => {});
  assert.deepEqual(historyCursor(buffer), { before: 2, offset: 8 });
  expandOutputHistory(buffer);
  const text = prependOutputPage(buffer, {
    chunks: [chunk(1, 'HEADER\n'), { id: 2, data: original.subarray(0, 8).toString('base64') }],
    has_more: false,
  });
  assert.equal(text, 'HEADER\nstart \u20acGREEN\n');
  assert.deepEqual(historyCursor(buffer), { before: 1, offset: 0 });
  assert.equal(buffer.after, 2);
  assert.equal(buffer.history.hasEarlier, false);
});

test('scrollback appends live output beyond 256 KiB while retaining older pages', async () => {
  const { appendOutputChunks, expandOutputHistory, prependOutputPage } =
    await import('./execution-output.js');
  const buffer = createOutputBuffer();
  buffer.hasEarlier = true;
  appendOutputChunks(
    buffer,
    Array.from({ length: 64 }, (_, index) => chunk(65 + index, 'n'.repeat(4096))),
  );
  expandOutputHistory(buffer);
  const text = prependOutputPage(buffer, {
    chunks: Array.from({ length: 64 }, (_, index) => chunk(1 + index, 'o'.repeat(4096))),
    has_more: false,
  });
  assert.equal(text.length, 524288);
  assert.equal(text, 'o'.repeat(262144) + 'n'.repeat(262144));
  appendOutputChunks(buffer, [chunk(129, 'new live output\n')]);
  assert.equal(buffer.history.text, text + 'new live output\n');
  assert.equal(buffer.history.chunks.at(-1).id, 129);
  assert.equal(buffer.history.hasEarlier, false);
  assert.equal(buffer.bytes, 262144);
  assert.equal(buffer.text.length, 262144);
  assert.match(buffer.text, /new live output\n$/);
  assert.equal(buffer.hasEarlier, true);
});

test('overlapping older pages are rejected without moving the history cursor', async () => {
  const { appendOutputChunks, historyCursor, prependOutputPage } =
    await import('./execution-output.js');
  const buffer = createOutputBuffer();
  appendOutputChunks(buffer, [chunk(10, 'current')]);
  assert.throws(
    () => prependOutputPage(buffer, { chunks: [chunk(11, 'wrong')], has_more: true }),
    /overlap/,
  );
  assert.deepEqual(historyCursor(buffer), { before: 10, offset: 0 });
});

test('scrolling away retains output as live chunks roll out of the bounded tail', async () => {
  const { appendOutputChunks, expandOutputHistory, historyCursor } =
    await import('./execution-output.js');
  const buffer = createOutputBuffer();
  appendOutputChunks(buffer, [chunk(1, 'first line\n')]);
  expandOutputHistory(buffer);
  const incoming = Array.from({ length: 80 }, (_, index) => chunk(index + 2, 'x'.repeat(4096)));
  appendOutputChunks(buffer, incoming);
  appendOutputChunks(buffer, incoming);
  assert.equal(buffer.history.text, 'first line\n' + 'x'.repeat(327680));
  assert.deepEqual(historyCursor(buffer), { before: 1, offset: 0 });
  assert.equal(buffer.history.chunks.length, 81);
  assert.equal(buffer.bytes, 262144);
  assert.equal(buffer.history.hasEarlier, false);
});

test('live decoding continues across prepends, split UTF-8, and terminal escape sequences', async () => {
  const { appendOutputChunks, expandOutputHistory, prependOutputPage } =
    await import('./execution-output.js');
  const buffer = createOutputBuffer();
  appendOutputChunks(buffer, [chunk(2, 'progress 10%\rprogress ')]);
  expandOutputHistory(buffer);
  appendOutputChunks(buffer, [{ id: 3, data: Buffer.from([0xe2, 0x82]).toString('base64') }]);
  prependOutputPage(buffer, { chunks: [chunk(1, 'HEADER\n')], has_more: false });
  appendOutputChunks(buffer, [
    { id: 4, data: Buffer.from([0xac, 0x1b, 0x5b, 0x33]).toString('base64') },
  ]);
  prependOutputPage(buffer, { chunks: [], has_more: false });
  appendOutputChunks(buffer, [chunk(5, '32mOK\u001b[0m\n')]);
  assert.equal(buffer.history.text, 'HEADER\nprogress \u20acOK\n');
  assert.equal(buffer.text, 'progress \u20acOK\n');
  assert.equal(buffer.after, 5);
});
