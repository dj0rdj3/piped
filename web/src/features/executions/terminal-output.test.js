import assert from 'node:assert/strict';
import test from 'node:test';
import { appendTerminalOutput } from './terminal-output.js';

function state() {
  return { text: '', cursor: 0, escape: '' };
}
test('terminal output handles packet boundaries, carriage returns, and backspace', () => {
  const output = state();
  appendTerminalOutput(output, '\u001b[3');
  appendTerminalOutput(output, '1mProgress 1%\rProgress 2%\u001b[0m\r\n');
  appendTerminalOutput(output, 'abd\bc\n');
  assert.equal(output.text, 'Progress 2%\nabc\n');
});
test('terminal output strips OSC links and control characters while preserving user text', () => {
  const output = state();
  appendTerminalOutput(output, '\u001b]8;;https://example.com');
  appendTerminalOutput(output, '\u001b\\<script>alert(1)</script>\u001b]8;;\u0007\u0003\u0000\n');
  appendTerminalOutput(output, '\u0416\u00e9\n');
  assert.equal(output.text, '<script>alert(1)</script>\n\u0416\u00e9\n');
});
test('visible output is bounded while the persisted cursor stays separate', () => {
  const output = state();
  appendTerminalOutput(output, 'x'.repeat(270000));
  assert.equal(output.text.length, 262144);
  assert.equal(output.cursor, 262144);
  assert.equal(output.trimmed, true);
});

test('high-volume short CRLF lines append in bulk and retain split line endings', () => {
  const output = state();
  for (let index = 0; index < 128; index++) {
    appendTerminalOutput(output, 'y\r\n'.repeat(1300));
  }
  assert.equal(output.text, 'y\n'.repeat(131072));
  appendTerminalOutput(output, 'next\r');
  appendTerminalOutput(output, '\nline\n');
  assert.match(output.text, /y\nnext\nline\n$/);
  assert.equal(output.cursor, output.text.length);
});
