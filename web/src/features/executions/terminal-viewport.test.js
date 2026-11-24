import assert from 'node:assert/strict';
import test from 'node:test';
import { createTerminalViewport } from './terminal-viewport.js';

function fixture() {
  const ticks = [];
  const terminal = { scrollTop: 0, scrollLeft: 0, scrollHeight: 1000, clientHeight: 100 };
  let following;
  const viewport = createTerminalViewport({
    element: () => terminal,
    nextTick: () => new Promise((resolve) => ticks.push(resolve)),
    onFollowing: (value) => (following = value),
  });
  return {
    viewport,
    terminal,
    get following() {
      return following;
    },
    async flush() {
      ticks.splice(0).forEach((resolve) => resolve());
      await Promise.resolve();
    },
  };
}

test('each host/run restores its own position and follow preference without another output packet', async () => {
  const f = fixture();
  const first = {};
  const second = {};
  void f.viewport.activate(first);
  await f.flush();
  f.viewport.pause();
  f.terminal.scrollTop = 240;
  f.terminal.scrollLeft = 12;
  f.viewport.scroll();
  void f.viewport.activate(second);
  await f.flush();
  assert.equal(f.following, true);
  f.terminal.scrollTop = 710;
  f.viewport.scroll();
  void f.viewport.activate(first);
  await f.flush();
  assert.equal(f.terminal.scrollTop, 240);
  assert.equal(f.terminal.scrollLeft, 12);
  assert.equal(f.following, false);
  void f.viewport.activate(second);
  await f.flush();
  assert.equal(f.terminal.scrollTop, 710);
  assert.equal(f.following, false);
});

test('scroll-up intent before a scheduled render prevents a jump back to the bottom', async () => {
  const f = fixture();
  const buffer = {};
  void f.viewport.activate(buffer);
  await f.flush();
  void f.viewport.rendered(buffer);
  f.viewport.pause();
  f.terminal.scrollTop = 650;
  f.viewport.scroll();
  f.terminal.scrollHeight = 1500;
  await f.flush();
  assert.equal(f.terminal.scrollTop, 650);
  assert.equal(f.following, false);
  f.viewport.follow();
  void f.viewport.rendered(buffer);
  await f.flush();
  assert.equal(f.terminal.scrollTop, 1500);
  assert.equal(f.following, true);
});

test('new output never restores a stale position over more recent user scrolling', async () => {
  const f = fixture();
  const buffer = {};
  void f.viewport.activate(buffer);
  await f.flush();
  f.viewport.pause();
  f.terminal.scrollTop = 500;
  f.viewport.scroll();
  void f.viewport.rendered(buffer);
  f.terminal.scrollTop = 300;
  f.viewport.scroll();
  await f.flush();
  assert.equal(f.terminal.scrollTop, 300);
  assert.equal(buffer.viewport.top, 300);
});

test('rapid selection and stale render callbacks cannot overwrite another terminal position', async () => {
  const f = fixture();
  const first = { viewport: { top: 150, left: 0, following: false } };
  const second = { viewport: { top: 350, left: 0, following: false } };
  void f.viewport.activate(first);
  void f.viewport.rendered(first);
  void f.viewport.activate(null);
  void f.viewport.activate(second);
  f.terminal.scrollTop = 0;
  f.viewport.scroll();
  await f.flush();
  assert.equal(first.viewport.top, 150);
  assert.equal(f.terminal.scrollTop, 350);
  assert.equal(f.following, false);
});
