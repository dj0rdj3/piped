import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mountComponent, deferred, settle } from '../../../test-support/component.js';

test('key creation requires scopes, guards duplicate requests, and clears secrets on unmount', async () => {
  const pending = deferred();
  const calls = [];
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { scopes: ['jobs:read'] },
    request(path, options) {
      calls.push({ path, options });
      return pending.promise;
    },
  });
  fixture.state.name = 'Automation';
  fixture.state.currentPassword = 'fixture-password';
  await fixture.state.submit();
  assert.equal(calls.length, 0);
  assert.match(fixture.state.error, /at least one scope/);
  fixture.state.selectedScopes = ['jobs:read'];
  const saving = fixture.state.submit();
  await fixture.state.submit();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, '/api-keys');
  assert.deepEqual(calls[0].options.body, {
    name: 'Automation',
    scopes: ['jobs:read'],
    expires_in_hours: 720,
    current_password: 'fixture-password',
  });
  pending.resolve({ token: 'fixture-token' });
  await saving;
  assert.equal(fixture.state.currentPassword, '');
  assert.equal(fixture.state.token, 'fixture-token');
  assert.equal(fixture.state.dirty, true);
  assert.deepEqual(fixture.events, [['saved']]);
  await fixture.state.submit();
  assert.equal(calls.length, 1);
  fixture.unmount();
  assert.equal(fixture.state.token, '');
});

test('failed key revocation retains the dialog and clears the password', async () => {
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { target: { id: 'key-one', name: 'Key' }, scopes: [] },
    request(path, options) {
      assert.equal(path, '/api-keys/key-one');
      assert.equal(options.method, 'DELETE');
      return Promise.reject(Object.assign(new Error('Invalid password'), { status: 401 }));
    },
  });
  try {
    fixture.state.currentPassword = 'incorrect';
    await fixture.state.submit();
    assert.equal(fixture.state.currentPassword, '');
    assert.equal(fixture.state.busy, false);
    assert.equal(fixture.state.error, 'Invalid password');
    assert.deepEqual(fixture.events, [['auth-error']]);
  } finally {
    fixture.unmount();
  }
});

test('leaving a pending key creation does not retain or reveal its late token', async () => {
  const pending = deferred();
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { scopes: ['users:read'] },
    request: () => pending.promise,
  });
  fixture.state.selectedScopes = ['users:read'];
  fixture.state.currentPassword = 'fixture';
  const saving = fixture.state.submit();
  fixture.unmount();
  pending.resolve({ token: 'late-fixture-token' });
  await saving;
  assert.equal(fixture.state.token, '');
  assert.equal(fixture.state.currentPassword, '');
  assert.deepEqual(fixture.events, []);
});

test('API Keys page ignores data arriving after navigation away', async () => {
  const pending = deferred();
  const fixture = await mountComponent('features/api-keys/APIKeysPage.vue', {
    request(path) {
      assert.equal(path, '/api-keys');
      return pending.promise;
    },
  });
  fixture.unmount();
  pending.resolve({ keys: [{ id: 'late-key' }], scopes: ['users:read'] });
  await settle();
  assert.deepEqual(fixture.state.keys, []);
  assert.deepEqual(fixture.state.scopes, []);
});

test('No expiration is sent explicitly as zero hours', async () => {
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { scopes: ['jobs:read'] },
    request(path, options) {
      assert.equal(path, '/api-keys');
      assert.equal(options.body.expires_in_hours, 0);
      return Promise.resolve({ token: 'fixture-token' });
    },
  });
  try {
    fixture.state.name = 'No expiration';
    fixture.state.selectedScopes = ['jobs:read'];
    fixture.state.expiresInHours = '0';
    fixture.state.currentPassword = 'fixture-password';
    await fixture.state.submit();
    assert.equal(fixture.state.token, 'fixture-token');
    assert.equal(fixture.state.currentPassword, '');
  } finally {
    fixture.unmount();
  }
});

test('successful revocation reports one save event and leaves closing to the page', async () => {
  const pending = deferred();
  let requests = 0;
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { target: { id: 'key-one', name: 'Key' }, scopes: [] },
    request(path, options) {
      requests++;
      assert.equal(path, '/api-keys/key-one');
      assert.equal(options.method, 'DELETE');
      assert.deepEqual(options.body, { current_password: 'fixture-password' });
      return pending.promise;
    },
  });
  try {
    fixture.state.currentPassword = 'fixture-password';
    const saving = fixture.state.submit();
    await fixture.state.submit();
    assert.equal(requests, 1);
    pending.resolve(null);
    await saving;
    assert.equal(fixture.state.currentPassword, '');
    assert.deepEqual(fixture.events, [['saved']]);
  } finally {
    fixture.unmount();
  }
});

test('the page keeps the one-time key visible after creation and closes a revoked key', async () => {
  let requests = 0;
  const fixture = await mountComponent('features/api-keys/APIKeysPage.vue', {
    request() {
      requests++;
      return Promise.resolve({ keys: [], scopes: ['users:read'] });
    },
  });
  try {
    await settle();
    fixture.state.dialog = {};
    fixture.state.saved();
    await settle();
    assert.deepEqual(fixture.state.dialog, {});
    assert.equal(fixture.state.notice, 'API key created.');
    fixture.state.dialog = { target: { id: 'key-one' } };
    fixture.state.saved();
    await settle();
    assert.equal(fixture.state.dialog, null);
    assert.equal(fixture.state.notice, 'API key revoked.');
    assert.equal(requests, 3);
  } finally {
    fixture.unmount();
  }
});

test('one-time key copying guards repeated clicks and cleans its feedback timer', async () => {
  const pending = deferred();
  let writes = 0;
  const timers = new Map();
  let nextTimer = 0;
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { scopes: [] },
    browser: {
      setTimeout(callback, delay) {
        assert.equal(delay, 2000);
        timers.set(++nextTimer, callback);
        return nextTimer;
      },
      clearTimeout(id) {
        timers.delete(id);
      },
    },
    globals: {
      navigator: {
        clipboard: {
          writeText(value) {
            writes++;
            assert.equal(value, 'fixture-one-time-token');
            return pending.promise;
          },
        },
      },
    },
  });
  fixture.state.token = 'fixture-one-time-token';
  const copying = fixture.state.copyToken();
  await fixture.state.copyToken();
  assert.equal(writes, 1);
  pending.resolve();
  await copying;
  assert.equal(fixture.state.copied, true);
  assert.equal(timers.size, 1);
  fixture.unmount();
  assert.equal(timers.size, 0);
  assert.equal(fixture.state.token, '');
});

test('a failed clipboard request preserves the one-time token for manual copying', async () => {
  const fixture = await mountComponent('features/api-keys/APIKeyDialog.vue', {
    props: { scopes: [] },
    globals: {
      navigator: { clipboard: { writeText: () => Promise.reject(new Error('Denied')) } },
    },
  });
  try {
    fixture.state.token = 'fixture-one-time-token';
    await fixture.state.copyToken();
    assert.match(fixture.state.error, /copy it manually/);
    assert.equal(fixture.state.token, 'fixture-one-time-token');
    assert.equal(fixture.state.copying, false);
    assert.equal(fixture.state.dirty, true);
  } finally {
    fixture.unmount();
  }
});
