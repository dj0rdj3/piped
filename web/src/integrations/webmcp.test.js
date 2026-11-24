import assert from 'node:assert/strict';
import { test } from 'node:test';
import { registerWebMCP } from './webmcp.js';

function harness(overrides = {}) {
  const tools = new Map();
  const registrations = [];
  const requests = [];
  const pages = [];
  const forms = [];
  let authErrors = 0;
  return {
    tools,
    registrations,
    requests,
    pages,
    forms,
    get authErrors() {
      return authErrors;
    },
    context: {
      registerTool(tool, options) {
        tools.set(tool.name, tool);
        registrations.push(options);
      },
    },
    options: {
      async request(path, options) {
        requests.push({ path, options });
        return { records: [] };
      },
      async navigate(path) {
        pages.push(path);
      },
      async openJob(action) {
        forms.push(action);
      },
      async openHost(action) {
        forms.push(action);
      },
      async openCredential(action) {
        forms.push(action);
      },
      authError() {
        authErrors++;
      },
      ...overrides,
    },
  };
}

test('unsupported browsers work without WebMCP', async () => {
  const stop = await registerWebMCP(undefined, {});
  stop();
});

test('WebMCP reads use browser APIs and forms require human completion', async () => {
  const h = harness();
  const stop = await registerWebMCP(h.context, h.options);
  assert.equal(h.tools.size, 12);
  for (const tool of h.tools.values()) {
    assert.equal(tool.annotations.readOnlyHint, true);
    assert.doesNotMatch(JSON.stringify(tool.inputSchema), /password|private_key|token/);
  }
  await h.tools.get('piped_list_hosts').execute({});
  await h.tools.get('piped_list_users').execute({});
  await h.tools.get('piped_list_credentials').execute({});
  assert.deepEqual(
    h.requests.map(({ path }) => path),
    ['/hosts', '/users', '/credentials'],
  );
  for (const { options } of h.requests) {
    assert.deepEqual(Object.keys(options), ['signal']);
    assert.equal(options.signal.aborted, false);
  }
  await h.tools.get('piped_open_page').execute({ page: 'credentials' });
  assert.deepEqual(h.pages, ['/credentials']);
  await assert.rejects(h.tools.get('piped_open_page').execute({ page: 'api-keys' }));
  const result = JSON.parse(
    await h.tools.get('piped_open_credential_form').execute({ mode: 'import', name: 'Deploy' }),
  );
  assert.deepEqual(h.forms, [{ mode: 'import', name: 'Deploy' }]);
  assert.equal(result.saved, false);
  assert.equal(result.user_confirmation_required, true);
  await assert.rejects(
    h.tools.get('piped_open_page').execute({ page: 'https://attacker.example' }),
  );
  await assert.rejects(h.tools.get('piped_open_credential_form').execute({ mode: 'delete' }));
  const hostResult = JSON.parse(
    await h.tools.get('piped_open_host_form').execute({ name: 'Server' }),
  );
  assert.deepEqual(h.forms.at(-1), { name: 'Server' });
  assert.equal(hostResult.saved, false);
  assert.equal(hostResult.user_confirmation_required, true);
  await h.tools.get('piped_open_page').execute({ page: 'hosts' });
  assert.equal(h.pages.at(-1), '/hosts');
  await assert.rejects(h.tools.get('piped_open_host_form').execute({ name: 'x'.repeat(101) }));
  await h.tools.get('piped_list_jobs').execute({});
  assert.equal(h.requests.at(-1).path, '/jobs');
  await h.tools.get('piped_open_page').execute({ page: 'jobs' });
  assert.equal(h.pages.at(-1), '/jobs');
  const jobResult = JSON.parse(
    await h.tools.get('piped_open_job_form').execute({ name: 'Maintenance' }),
  );
  assert.deepEqual(h.forms.at(-1), { name: 'Maintenance' });
  assert.equal(jobResult.saved, false);
  assert.equal(jobResult.user_confirmation_required, true);
  await assert.rejects(h.tools.get('piped_open_job_form').execute({ name: 'x'.repeat(101) }));
  stop();
  assert.equal(h.registrations[0].signal.aborted, true);
  for (const tool of h.tools.values()) {
    await assert.rejects(tool.execute({ page: 'users', mode: 'generate' }), /Sign in required/);
  }
});

test('logout blocks an in-flight read result and removes legacy registrations', async () => {
  let resolve;
  const h = harness({
    request: () =>
      new Promise((done) => {
        resolve = done;
      }),
  });
  const removed = [];
  h.context.unregisterTool = (name) => removed.push(name);
  const stop = await registerWebMCP(h.context, h.options);
  const pending = h.tools.get('piped_list_users').execute({});
  stop();
  resolve({ users: [{ username: 'old-account' }] });
  await assert.rejects(pending, /Sign in required/);
  assert.deepEqual(removed, [...h.tools.keys()]);
});

test('external cancellation during registration prevents stale-account tools', async () => {
  let resolve;
  const h = harness();
  h.context.registerTool = (tool) => {
    h.tools.set(tool.name, tool);
    return new Promise((done) => {
      resolve = done;
    });
  };
  const controller = new AbortController();
  const pending = registerWebMCP(h.context, { ...h.options, signal: controller.signal });
  controller.abort();
  await assert.rejects(h.tools.values().next().value.execute({}), /Sign in required/);
  resolve();
  await pending;
  assert.equal(h.tools.size, 1);
  assert.equal(h.requests.length, 0);
});

test('expired sessions notify the app and failed registrations roll back', async () => {
  const h = harness({
    request: async () => {
      throw Object.assign(new Error('Expired'), { status: 401 });
    },
  });
  const stop = await registerWebMCP(h.context, h.options);
  await assert.rejects(h.tools.get('piped_list_users').execute({}), /Expired/);
  assert.equal(h.authErrors, 1);
  stop();

  const failing = harness();
  const original = failing.context.registerTool;
  failing.context.registerTool = (tool, options) => {
    if (failing.tools.size) {
      throw new Error('Registration failed');
    }
    original(tool, options);
  };
  await assert.rejects(registerWebMCP(failing.context, failing.options), /Registration failed/);
  assert.equal(failing.registrations[0].signal.aborted, true);
  await assert.rejects(failing.tools.values().next().value.execute({}), /Sign in required/);
});

test('execution reads validate IDs and cursors before requesting history', async () => {
  const h = harness();
  const stop = await registerWebMCP(h.context, h.options);
  const jobID = 'a'.repeat(32);
  const runID = 'b'.repeat(32);
  const hostID = 'c'.repeat(32);
  await h.tools.get('piped_list_runs').execute({ job_id: jobID });
  await h.tools.get('piped_get_run').execute({ run_id: runID });
  await h.tools.get('piped_read_run_output').execute({ run_id: runID, host_id: hostID, after: 41 });
  assert.deepEqual(
    h.requests.map(({ path }) => path),
    [`/jobs/${jobID}/runs`, `/runs/${runID}`, `/runs/${runID}/hosts/${hostID}/output?after=41`],
  );
  await h.tools
    .get('piped_read_run_output')
    .execute({ run_id: runID, host_id: hostID, tail: true });
  assert.equal(h.requests.at(-1).path, `/runs/${runID}/hosts/${hostID}/output?tail=1`);
  await assert.rejects(
    h.tools
      .get('piped_read_run_output')
      .execute({ run_id: runID, host_id: hostID, tail: true, after: 1 }),
  );
  await assert.rejects(h.tools.get('piped_list_runs').execute({ job_id: '../users' }));
  await assert.rejects(h.tools.get('piped_get_run').execute({ run_id: 'invalid' }));
  await assert.rejects(
    h.tools.get('piped_read_run_output').execute({ run_id: runID, host_id: hostID, after: -1 }),
  );
  stop();
});

test('schedule preview is a bounded read with encoded cron input', async () => {
  const h = harness();
  const stop = await registerWebMCP(h.context, h.options);
  const preview = h.tools.get('piped_preview_schedule');
  await preview.execute({ cron: '*/5 * * * MON-FRI' });
  assert.equal(
    h.requests[0].path,
    '/jobs/schedule-preview?cron=' + encodeURIComponent('*/5 * * * MON-FRI'),
  );
  for (const cron of ['', null, 12, 'x'.repeat(257)]) {
    await assert.rejects(preview.execute({ cron }));
  }
  assert.equal(h.requests.length, 1);
  stop();
});
