import assert from 'node:assert/strict';
import test from 'node:test';
import { createTerminalLayout } from './terminal-layout.js';

test('windowed output indexes large short-line logs without rendering all rows', () => {
  const layout = createTerminalLayout();
  const text = 'y\n'.repeat(500000);
  layout.update(text, 120);
  assert.equal(layout.count, 500001);
  assert.equal(layout.window(400000, 40), Array(40).fill('y').join('\n'));
  layout.update(text + 'done\n', 120);
  assert.equal(layout.count, 500002);
  assert.equal(layout.window(500000, 2), 'done\n');
});

test('append updates recalculate the partial final row and checkpoint boundaries', () => {
  const layout = createTerminalLayout();
  let text = '';
  for (let index = 0; index < 600; index++) {
    text += `${index}\n`;
    layout.update(text, 3);
  }
  assert.equal(layout.count, 601);
  assert.equal(layout.window(255, 3), '255\n256\n257');
  layout.update(text + 'abcdef', 3);
  assert.equal(layout.window(599, 5), '599\nabc\ndef');
  layout.update(text + 'abcdefg', 3);
  assert.equal(layout.window(600, 5), 'abc\ndef\ng');
});

test('replacements, prepends, and width changes rebuild the display index', () => {
  const layout = createTerminalLayout();
  layout.update('abcd\nefgh', 4);
  layout.update('HEADER\nabcd\nefgh', 4);
  assert.equal(layout.window(0, 10), 'HEAD\nER\nabcd\nefgh');
  layout.update('abcdef', 2);
  assert.equal(layout.count, 3);
  assert.equal(layout.window(0, 10), 'ab\ncd\nef');
  layout.update('', 0);
  assert.equal(layout.count, 1);
  assert.equal(layout.window(0, 10), '');
});

test('Unicode graphemes are wrapped using measured widths without splitting combining text', () => {
  const layout = createTerminalLayout();
  layout.update('a\u754c\u754ce\u0301x', 3, (value) => (value === '\u754c' ? 2 : 1));
  assert.equal(layout.window(0, 10), 'a\u754c\n\u754ce\u0301\nx');
  layout.update('x\tvalue', 12);
  assert.equal(layout.window(0, 10), 'x\tvalu\ne');
});
