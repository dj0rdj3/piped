import { useJobDraft } from '../jobs/useJobDraft.js';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mountModel, mountComponent, deferred, settle } from '../../../test-support/component.js';

const target = {
  id: 'target',
  name: 'Chat',
  method: 'POST',
  content_type: 'text/plain',
  body: '$job: $status',
  enabled: true,
  default_enabled: true,
  version: 3,
};

test('notification edits load readable templates, preserve stored secrets, and clear replacements', async (t) => {
  const calls = [];
  const endpoint = {
    url: 'https://example.internal/$secret',
    headers: { Authorization: 'Bearer $secret' },
  };
  const fixture = await mountComponent('features/notifications/WebhookDialog.vue', {
    props: { mode: 'edit', target: { ...target, endpoint, secret_names: ['secret'] } },
    request(path, options) {
      calls.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  assert.equal(fixture.state.form.url, endpoint.url);
  assert.deepEqual(JSON.parse(fixture.state.form.headers), endpoint.headers);
  fixture.state.form.name = 'Changed';
  await fixture.state.submit();
  assert.equal(calls[0].path, '/notifications/target');
  assert.equal(calls[0].method, 'PUT');
  assert.equal(calls[0].body.version, 3);
  assert.deepEqual(calls[0].body.endpoint, endpoint);
  assert.deepEqual(calls[0].body.secrets, [{ name: 'secret' }]);
  fixture.state.form.secrets[0].value = 'replacement-private-value';
  await fixture.state.submit();
  assert.deepEqual(calls[1].body.secrets, [{ name: 'secret', value: 'replacement-private-value' }]);
  assert.equal(fixture.state.form.secrets[0].value, '');
  fixture.state.form.secrets[0].value = 'unsaved-private-value';
  fixture.unmount();
  assert.equal(fixture.state.form.secrets[0].value, '');
});

test('invalid headers block writes and duplicate notification submissions are guarded', async () => {
  const pending = deferred();
  let calls = 0;
  const fixture = await mountComponent('features/notifications/WebhookDialog.vue', {
    props: { mode: 'create' },
    request() {
      calls++;
      return pending.promise;
    },
  });
  fixture.state.form.headers = '{"Authorization":1}';
  await fixture.state.submit();
  assert.equal(calls, 0);
  assert.match(fixture.state.error, /JSON object/);
  fixture.state.form.headers = '{}';
  const saving = fixture.state.submit();
  await fixture.state.submit();
  assert.equal(calls, 1);
  fixture.unmount();
  pending.resolve({});
  await saving;
  assert.deepEqual(fixture.events, []);
});

test('webhook dirty state tracks form changes and ignores secret row identities', async (t) => {
  const fixture = await mountComponent('features/notifications/WebhookDialog.vue', {
    props: { mode: 'edit', target: { ...target, secret_names: ['secret'] } },
  });
  t.after(fixture.unmount);
  assert.equal(fixture.state.dirty, false);
  fixture.state.form.secrets[0].key = 42;
  assert.equal(fixture.state.dirty, false);
  fixture.state.form.secrets[0].value = 'replacement';
  assert.equal(fixture.state.dirty, true);
  fixture.state.form.secrets[0].value = '';
  assert.equal(fixture.state.dirty, false);
  fixture.state.form.defaultEnabled = false;
  assert.equal(fixture.state.dirty, true);
  fixture.state.form.defaultEnabled = true;
  assert.equal(fixture.state.dirty, false);
});

test('new jobs preselect enabled defaults while edits and copies retain their selections', async (t) => {
  for (const mode of ['create', 'edit', 'duplicate']) {
    const calls = [];
    const fixture = await mountModel(useJobDraft, {
      props: {
        mode,
        target:
          mode === 'create'
            ? null
            : {
                id: 'job',
                name: 'Job',
                version: 2,
                script: 'true',
                host_ids: ['host'],
                notification_target_ids: ['optional'],
              },
      },
      request(path, options) {
        if (path === '/notifications') {
          return Promise.resolve({
            targets: [
              target,
              { ...target, id: 'disabled', enabled: false },
              { ...target, id: 'optional', default_enabled: false },
            ],
          });
        }
        if (path === '/hosts') {
          return Promise.resolve({ hosts: [{ id: 'host', name: 'Host', tags: [] }] });
        }
        calls.push({ path, ...options });
        return Promise.resolve();
      },
    });
    t.after(fixture.unmount);
    await settle();
    assert.deepEqual(fixture.state.notificationIDs, mode === 'create' ? ['target'] : ['optional']);
    if (mode === 'create') {
      assert.equal(
        fixture.state.dirty,
        false,
        'Loading default targets must not dirty a new form.',
      );
    }
    fixture.state.name = 'Job';
    fixture.state.script = 'true';
    fixture.state.hostIDs = ['host'];
    fixture.state.notificationIDs = [];
    await fixture.state.submit();
    assert.deepEqual(calls[0].body.notification_target_ids, []);
  }
});
