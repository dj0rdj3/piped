import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readKeyFile } from './key-files.js';

test('cancelling a key picker leaves existing pasted content untouched', async () => {
  let content = 'already pasted key';
  const selected = await readKeyFile({ files: [], value: '' }, 32768);
  if (selected !== null) {
    content = selected;
  }
  assert.equal(content, 'already pasted key');
  assert.equal(await readKeyFile({ value: '' }, 32768), null);
});

test('public and private key files load text and reset the picker for reselection', async () => {
  for (const [content, limit] of [
    ['ssh-ed25519 test user@host', 8192],
    ['-----BEGIN OPENSSH PRIVATE KEY-----\ncontents\n', 32768],
  ]) {
    const input = { value: 'selected-file', files: [new File([content], 'key')] };
    assert.equal(await readKeyFile(input, limit), content);
    assert.equal(input.value, '');
  }
});

test('oversized and unreadable files fail without exposing file contents', async () => {
  const oversized = {
    files: [
      {
        size: 8193,
        text() {
          throw new Error('must not read oversized file');
        },
      },
    ],
    value: 'key',
  };
  await assert.rejects(readKeyFile(oversized, 8192), /at most 8 KiB/);
  const broken = {
    files: [
      {
        size: 1,
        async text() {
          throw new Error('sensitive content');
        },
      },
    ],
    value: 'key',
  };
  await assert.rejects(
    readKeyFile(broken, 32768),
    (err) => err.message === 'Could not read the key file.',
  );
});
