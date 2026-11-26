import assert from 'node:assert/strict';
import test from 'node:test';
import { mountModel, deferred, settle } from '../../../test-support/component.js';
import { useExecutionSession } from './useExecutionSession.js';

async function session() {
  const mutation = deferred();
  const run = {
    id: 'run-one',
    finished_at: 1,
    hosts: [
      { host_id: 'first', status: 'running' },
      { host_id: 'second', status: 'running' },
    ],
  };
  const fixture = mountModel(useExecutionSession, {
    props: { job: { id: 'job' }, initialRun: run },
    browser: { cancelAnimationFrame() {} },
    prepare(state) {
      state.dialog.value = { showModal() {} };
    },
    request(path, options = {}) {
      if (options.method === 'POST') {
        return mutation.promise;
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
  await settle();
  return { ...fixture, mutation };
}

test('late control failures cannot update a new host selection', async (t) => {
  const fixture = await session();
  t.after(fixture.unmount);
  const saving = fixture.state.action('cancel');
  fixture.state.hostID = 'second';
  fixture.state.selectHost();
  fixture.mutation.reject(Object.assign(new Error('old selection failed'), { status: 401 }));
  assert.equal(await saving, false);
  assert.equal(fixture.state.error, '');
  assert.equal(
    fixture.events.some(([name]) => name === 'signed-out'),
    false,
  );
});

test('unmount clears input and ignores late control authentication errors', async () => {
  const fixture = await session();
  fixture.state.input = 'private draft';
  const saving = fixture.state.action('cancel');
  fixture.unmount();
  fixture.mutation.reject(Object.assign(new Error('expired old request'), { status: 401 }));
  assert.equal(await saving, false);
  assert.equal(fixture.state.input, '');
  assert.equal(
    fixture.events.some(([name]) => name === 'signed-out'),
    false,
  );
});
