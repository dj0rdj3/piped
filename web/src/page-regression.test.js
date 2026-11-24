import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mountComponent, deferred, settle } from '../test-support/component.js';

const currentUser = { id: 1, username: 'fixture' };
const host = { id: 'host-one', name: 'Host', tags: [] };
const job = {
  id: 'job-one',
  name: 'Job',
  version: 2,
  script: 'printf hello',
  host_ids: [host.id],
  secret_names: ['TOKEN'],
  schedule: { cron: '0 2 * * *', enabled: true },
};

test('sign-in guards duplicate submissions and clears the password on success and failure', async (t) => {
  const pending = [];
  const fixture = await mountComponent('features/auth/LoginForm.vue', {
    request(path, options) {
      const response = deferred();
      pending.push({ path, options, ...response });
      return response.promise;
    },
  });
  t.after(fixture.unmount);
  fixture.state.username = currentUser.username;
  fixture.state.password = 'fixture-password';
  const saved = fixture.state.submit();
  await fixture.state.submit();
  assert.equal(pending.length, 1);
  assert.equal(pending[0].path, '/auth/login');
  pending[0].resolve({ user: currentUser, development: true });
  await saved;
  assert.equal(fixture.state.password, '');
  assert.deepEqual(fixture.events, [['authenticated', currentUser, true]]);
  fixture.state.password = 'rejected-password';
  const failed = fixture.state.submit();
  pending[1].reject(new Error('Sign-in failed.'));
  await failed;
  assert.equal(fixture.state.password, '');
  assert.equal(fixture.state.error, 'Sign-in failed.');
  assert.equal(fixture.state.busy, false);
});

for (const component of ['CredentialsPage', 'CredentialDialog']) {
  test(`${component} keeps public-key copy guarded and cleans feedback on unmount`, async () => {
    const writes = [];
    const timers = new Map();
    let sequence = 0;
    const credential = {
      id: 'credential-one',
      name: 'Fixture',
      public_key: 'ssh-ed25519 PUBLIC fixture-comment',
    };
    const fixture = await mountComponent('features/credentials/' + component + '.vue', {
      props: component === 'CredentialDialog' ? { mode: 'public', target: credential } : {},
      request: () => Promise.resolve({ credentials: [] }),
      browser: {
        setTimeout(callback, delay) {
          assert.equal(delay, 2000);
          timers.set(++sequence, callback);
          return sequence;
        },
        clearTimeout(id) {
          timers.delete(id);
        },
      },
      globals: {
        navigator: {
          clipboard: {
            writeText(value) {
              const pending = deferred();
              writes.push({ value, ...pending });
              return pending.promise;
            },
          },
        },
      },
    });
    try {
      const copying = fixture.state.copyPublicKey(credential);
      await fixture.state.copyPublicKey(credential);
      assert.equal(writes.length, 1);
      assert.equal(writes[0].value, credential.public_key);
      assert.equal(fixture.state.copying, true);
      writes[0].resolve();
      await copying;
      assert.equal(
        component === 'CredentialDialog'
          ? fixture.state.copied
          : fixture.state.copiedID === credential.id,
        true,
      );
      for (const [id, callback] of timers) {
        timers.delete(id);
        callback();
      }
      const late = fixture.state.copyPublicKey(credential);
      fixture.unmount();
      writes[1].resolve();
      await late;
      assert.equal(timers.size, 0);
      assert.equal(
        component === 'CredentialDialog' ? fixture.state.copied : !!fixture.state.copiedID,
        false,
      );
    } catch (error) {
      fixture.unmount();
      throw error;
    }
  });
}

for (const [component, resource, load, loading, props] of [
  ['hosts/HostsPage', 'hosts', 'load', 'loading', {}],
  ['credentials/CredentialsPage', 'credentials', 'load', 'loading', {}],
  ['users/UsersPage', 'users', 'load', 'loading', { currentUser }],
  ['hosts/HostDialog', 'credentials', 'loadCredentials', 'loadingCredentials', { mode: 'create' }],
  ['jobs/JobDialog', 'hosts', 'loadHosts', 'loadingHosts', { mode: 'create' }],
]) {
  test(`${component} ignores stale refreshes and late authorization errors after unmount`, async () => {
    const pending = [];
    const fixture = await mountComponent('features/' + component + '.vue', {
      props,
      request() {
        const response = deferred();
        pending.push(response);
        return response.promise;
      },
    });
    try {
      const refresh = fixture.state[load]();
      pending[1].resolve({ [resource]: [{ id: 'newest', tags: [] }] });
      await refresh;
      pending[0].resolve({ [resource]: [{ id: 'stale', tags: [] }] });
      await settle();
      assert.equal(fixture.state[resource][0].id, 'newest');
      assert.equal(fixture.state[loading], false);
      const late = fixture.state[load]();
      fixture.unmount();
      pending[2].reject(Object.assign(new Error('Expired'), { status: 401 }));
      await late;
      assert.deepEqual(fixture.events, []);
    } catch (error) {
      fixture.unmount();
      throw error;
    }
  });
}

test('delete host and job dialogs do not fetch unrelated form choices', async () => {
  for (const component of ['hosts/HostDialog', 'jobs/JobDialog']) {
    const fixture = await mountComponent('features/' + component + '.vue', {
      props: { mode: 'delete', target: job },
      request() {
        assert.fail('Delete dialog requested form choices.');
      },
    });
    fixture.unmount();
  }
});

test('Jobs background refreshes cannot overwrite newer summaries or emit a stale sign-out', async (t) => {
  const summaries = [];
  let initialized = false;
  const fixture = await mountComponent('features/jobs/JobsPage.vue', {
    props: { currentUserId: 1 },
    browser: { setInterval: () => 1, clearInterval() {} },
    globals: { document: { visibilityState: 'visible' } },
    request(path) {
      if (path === '/hosts') {
        return Promise.resolve({ hosts: [host] });
      }
      if (!initialized) {
        initialized = true;
        return Promise.resolve({ jobs: [job] });
      }
      const response = deferred();
      summaries.push(response);
      return response.promise;
    },
  });
  t.after(fixture.unmount);
  await settle();
  const old = fixture.state.refreshSummaries();
  const latest = fixture.state.refreshSummaries();
  summaries[1].resolve({ jobs: [{ ...job, name: 'Latest' }] });
  await latest;
  summaries[0].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await old;
  assert.equal(fixture.state.jobs[0].name, 'Latest');
  assert.deepEqual(fixture.events, []);
});

test('host edits preserve omitted secrets and explicitly clear the sudo password', async (t) => {
  const writes = [];
  const fixture = await mountComponent('features/hosts/HostDialog.vue', {
    props: {
      mode: 'edit',
      target: {
        ...host,
        version: 3,
        address: '127.0.0.1',
        username: 'fixture',
        has_password: true,
        run_as_sudo: true,
      },
    },
    request(path, options) {
      if (!options) {
        return Promise.resolve({ credentials: [] });
      }
      writes.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  await settle();
  await fixture.state.submit();
  assert.equal(writes[0].method, 'PUT');
  assert.equal(writes[0].body.version, 3);
  assert.equal(Object.hasOwn(writes[0].body, 'password'), false);
  assert.equal(Object.hasOwn(writes[0].body, 'sudo_password'), false);
  fixture.state.clearSudoPassword = true;
  await fixture.state.submit();
  assert.equal(writes[1].body.sudo_password, '');
});

test('job duplication preserves stored secrets and cron but starts with scheduling disabled', async (t) => {
  const writes = [];
  const fixture = await mountComponent('features/jobs/JobDialog.vue', {
    props: { mode: 'duplicate', target: job },
    browser: { setTimeout: () => 1, clearTimeout() {} },
    request(path, options) {
      if (!options) {
        return Promise.resolve({ hosts: [host] });
      }
      writes.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.equal(fixture.state.canSave, true);
  await fixture.state.submit();
  assert.deepEqual(writes[0].body.copy_from, { id: job.id, version: job.version });
  assert.deepEqual(writes[0].body.secrets, [{ name: 'TOKEN' }]);
  assert.deepEqual(writes[0].body.schedule, { cron: '0 2 * * *', enabled: false });
  assert.equal(writes[0].body.version, 0);
  fixture.state.script = '\u65e5'.repeat(22000);
  await fixture.state.submit();
  assert.equal(writes.length, 1, 'UTF-8 byte limit was bypassed.');
});

test('user password confirmation and current-account sign-out remain enforced', async (t) => {
  const writes = [];
  const fixture = await mountComponent('features/users/UserDialog.vue', {
    props: { mode: 'password', target: currentUser, currentUser },
    request(path, options) {
      writes.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  fixture.state.password = 'fixture-new-password';
  fixture.state.confirmation = 'different-password';
  await fixture.state.submit();
  assert.equal(writes.length, 0);
  fixture.state.confirmation = fixture.state.password;
  fixture.state.currentPassword = 'fixture-current-password';
  await fixture.state.submit();
  assert.equal(writes[0].body.current_password, 'fixture-current-password');
  assert.equal(fixture.state.currentPassword, '');
  assert.deepEqual(fixture.events, [['signed-out']]);
});

test('a host-key decision stays pinned to its host and cannot clear a newer approval', async (t) => {
  const decision = deferred();
  const writes = [];
  const run = {
    id: 'run-one',
    finished_at: 1,
    hosts: [{ host_id: 'selected-host', status: 'succeeded' }],
  };
  const fixture = await mountComponent('features/executions/ExecutionDialog.vue', {
    props: { job, initialRun: run },
    browser: { cancelAnimationFrame() {} },
    prepare(state) {
      state.dialog.value = { showModal() {} };
    },
    request(path, options) {
      if (options.method === 'POST') {
        writes.push({ path, ...options });
        return decision.promise;
      }
      if (path.endsWith('/runs')) {
        return Promise.resolve({ runs: [run] });
      }
      if (path.includes('/output')) {
        return Promise.resolve({ chunks: [], truncated: false });
      }
      return Promise.resolve({ run, host_keys: [] });
    },
  });
  t.after(fixture.unmount);
  await settle();
  fixture.state.approval = { host_id: 'approved-host', fingerprint: 'reviewed-fingerprint' };
  const saving = fixture.state.decide(true);
  await fixture.state.action('cancel');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].path, '/runs/run-one/hosts/approved-host/host-key');
  assert.deepEqual(writes[0].body, { fingerprint: 'reviewed-fingerprint', accept: true });
  fixture.state.approval = { host_id: 'different-host', fingerprint: 'new-fingerprint' };
  decision.resolve();
  await saving;
  assert.equal(fixture.state.approval.fingerprint, 'new-fingerprint');
  assert.equal(fixture.state.busy, false);
});
