import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createRenderer } from 'vue';
import { usePageLoad } from './usePageLoad.js';

function pageHarness(t) {
  const requests = [];
  const results = [];
  let signedOut = 0;
  let page;
  const renderer = createRenderer({
    createComment: () => ({}),
    insert() {},
    remove() {},
    parentNode: () => null,
    nextSibling: () => null,
  });
  const app = renderer.createApp({
    setup() {
      page = usePageLoad(
        () =>
          new Promise((resolve, reject) => {
            requests.push({ resolve, reject });
          }),
        (result) => results.push(result),
        () => signedOut++,
      );
      return () => null;
    },
  });
  app.mount({});
  let mounted = true;
  function unmount() {
    if (mounted) {
      app.unmount();
      mounted = false;
    }
  }
  t.after(unmount);
  return { page, requests, results, unmount, signedOut: () => signedOut };
}

function failure(status) {
  return Object.assign(new Error('Request failed.'), { status });
}

test('page load starts on mount, reports errors, and clears them on retry', async (t) => {
  const { page, requests, results } = pageHarness(t);
  assert.equal(requests.length, 1);
  assert.equal(page.loading.value, true);
  requests[0].reject(failure(500));
  await Promise.resolve();
  assert.equal(page.error.value, 'Request failed.');
  assert.equal(page.loading.value, false);

  const retry = page.load();
  assert.equal(page.error.value, '');
  assert.equal(page.loading.value, true);
  requests[1].resolve({ users: [] });
  await retry;
  assert.deepEqual(results, [{ users: [] }]);
  assert.equal(page.loading.value, false);
});

for (const outcome of ['success', 'failure', 'unauthorized']) {
  test(`superseded ${outcome} cannot update the page or sign out`, async (t) => {
    const { page, requests, results, signedOut } = pageHarness(t);
    const latest = page.load();
    if (outcome === 'success') {
      requests[0].resolve('stale');
    } else {
      requests[0].reject(failure(outcome === 'unauthorized' ? 401 : 500));
    }
    await Promise.resolve();
    assert.deepEqual(results, []);
    assert.equal(page.loading.value, true);
    assert.equal(page.error.value, '');
    assert.equal(signedOut(), 0);
    requests[1].resolve('latest');
    await latest;
    assert.deepEqual(results, ['latest']);
    assert.equal(page.loading.value, false);
  });
}

test('a late response cannot overwrite a newer completed refresh', async (t) => {
  const { page, requests, results } = pageHarness(t);
  const refresh = page.load();
  requests[1].resolve('new');
  await refresh;
  requests[0].resolve('old');
  await Promise.resolve();
  assert.deepEqual(results, ['new']);
  assert.equal(page.loading.value, false);
});

test('current unauthorized responses preserve session-expiry handling', async (t) => {
  const { page, requests, signedOut } = pageHarness(t);
  requests[0].reject(failure(401));
  await Promise.resolve();
  assert.equal(signedOut(), 1);
  assert.equal(page.error.value, '');
  assert.equal(page.loading.value, false);
});

for (const outcome of ['success', 'failure', 'unauthorized']) {
  test(`unmounted pages ignore pending ${outcome}`, async (t) => {
    const { page, requests, results, unmount, signedOut } = pageHarness(t);
    unmount();
    if (outcome === 'success') {
      requests[0].resolve('late');
    } else {
      requests[0].reject(failure(outcome === 'unauthorized' ? 401 : 500));
    }
    await Promise.resolve();
    assert.deepEqual(results, []);
    assert.equal(page.error.value, '');
    assert.equal(signedOut(), 0);
  });
}
