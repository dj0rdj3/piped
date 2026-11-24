import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createBackdropDismissal } from './dialog-backdrop.js';

function fixture() {
  const dialog = {
    getBoundingClientRect: () => ({ left: 100, right: 500, top: 100, bottom: 500 }),
  };
  let closed = 0;
  return {
    backdrop: createBackdropDismissal(() => closed++),
    event: (clientX, clientY, target = dialog) => ({
      clientX,
      clientY,
      target,
      currentTarget: dialog,
    }),
    closed: () => closed,
  };
}

test('only a complete backdrop click dismisses a dialog', () => {
  const f = fixture();
  f.backdrop.click(f.event(50, 200));
  assert.equal(f.closed(), 0);
  for (const position of [
    [50, 200],
    [550, 200],
    [200, 50],
    [200, 550],
  ]) {
    f.backdrop.pointerDown(f.event(...position));
    f.backdrop.click(f.event(...position));
  }
  assert.equal(f.closed(), 4);
  f.backdrop.click(f.event(50, 200));
  assert.equal(f.closed(), 4);
});

test('dialog padding, borders, and descendant controls stay open', () => {
  const f = fixture();
  for (const position of [
    [100, 200],
    [500, 200],
    [200, 100],
    [200, 500],
    [200, 200],
  ]) {
    f.backdrop.pointerDown(f.event(...position));
    f.backdrop.click(f.event(...position));
  }
  const nestedDialog = {};
  f.backdrop.pointerDown(f.event(50, 200, nestedDialog));
  f.backdrop.click(f.event(50, 200, nestedDialog));
  assert.equal(f.closed(), 0);
});

test('drags across the edge and cancelled gestures do not dismiss', () => {
  const f = fixture();
  f.backdrop.pointerDown(f.event(200, 200, {}));
  f.backdrop.click(f.event(50, 200));
  f.backdrop.pointerDown(f.event(50, 200));
  f.backdrop.click(f.event(200, 200));
  f.backdrop.pointerDown(f.event(50, 200));
  f.backdrop.pointerCancel();
  f.backdrop.click(f.event(50, 200));
  f.backdrop.pointerDown(f.event(50, 200));
  f.backdrop.click(f.event(50, 200, {}));
  assert.equal(f.closed(), 0);
});
