import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createRenderer, nextTick, ref } from 'vue';
import { useInfiniteTable } from './useInfiniteTable.js';
import { deferred, settle } from '../../test-support/component.js';

function harness(t, field = 'hosts', options = {}) {
  const requests = [];
  let page;
  let signedOut = 0;
  const renderer = createRenderer({
    createComment: () => ({}),
    insert() {},
    remove() {},
    parentNode: () => null,
    nextSibling: () => null,
  });
  const app = renderer.createApp({
    setup() {
      page = useInfiniteTable(
        (params, signal) => {
          const pending = deferred();
          requests.push({ params, signal, ...pending });
          return pending.promise;
        },
        field,
        () => signedOut++,
        options,
      );
      return () => null;
    },
  });
  app.mount({});
  t.after(() => app.unmount());
  return { page, requests, unmount: () => app.unmount(), signedOut: () => signedOut };
}

function response(field, start, size, count = 500) {
  const end = Math.min(count, start + size);
  return {
    [field]: Array.from({ length: end - start }, (_, index) => ({ id: start + index })),
    page: { total: count, matched: count, next_cursor: end < count ? String(end) : '' },
  };
}

for (const field of ['hosts', 'jobs', 'credentials', 'users', 'targets', 'keys']) {
  test(`${field}: 500 records survive rapid scrolling and delayed pages without duplicates`, async (t) => {
    const { page, requests } = harness(t, field);
    let index = 0;
    let loaded = 0;
    while (loaded < 500) {
      const pending = requests[index];
      const size = Number(pending.params.get('limit'));
      assert.ok(size >= 8 && size <= 200);
      assert.equal(Number(pending.params.get('cursor') || 0), loaded);
      assert.equal(page.loading.value, true);
      for (let scroll = 0; scroll < 100; scroll++) {
        void page.loadMore();
      }
      assert.equal(requests.length, index + 1, 'overlapping page requests');
      pending.resolve(response(field, loaded, size));
      await settle();
      loaded = Math.min(500, loaded + size);
      assert.equal(page.rows.value.length, loaded);
      assert.equal(page.loading.value, false);
      assert.equal(new Set(page.rows.value.map((row) => row.id)).size, loaded);
      assert.equal(page.total.value, 500);
      if (loaded < 500) {
        void page.loadMore();
        index++;
      }
    }
    assert.deepEqual(
      page.rows.value.map((row) => row.id),
      Array.from({ length: 500 }, (_, i) => i),
    );
    await page.loadMore();
    assert.equal(requests.length, index + 1, 'end of list requested again');
  });
}

test('unavailable server preserves loaded rows, stops automatic retries and retries the same cursor', async (t) => {
  const { page, requests } = harness(t);
  requests[0].resolve(response('hosts', 0, 10));
  await settle();
  const loading = page.loadMore();
  requests[1].reject(new Error('Server unavailable.'));
  await loading;
  assert.equal(page.rows.value.length, 10);
  assert.equal(page.loading.value, false);
  assert.equal(page.error.value, 'Server unavailable.');
  for (let scroll = 0; scroll < 100; scroll++) {
    await page.loadMore();
  }
  assert.equal(requests.length, 2);
  const retry = page.retry();
  assert.equal(page.loading.value, true);
  assert.equal(requests[2].params.get('cursor'), '10');
  requests[2].resolve(response('hosts', 10, 10));
  await retry;
  assert.equal(page.rows.value.length, 20);
  assert.equal(page.error.value, '');
});

test('search aborts old pages and only the newest filter can update rows or sign out', async (t) => {
  const query = ref('');
  const { page, requests, signedOut } = harness(t, 'hosts', {
    filters: () => ({ q: query.value }),
  });
  query.value = 'last record';
  await nextTick();
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(page.loading.value, true);
  const latest = page.load();
  assert.equal(requests[1].params.get('q'), 'last record');
  requests[1].resolve(response('hosts', 499, 1));
  await latest;
  requests[0].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await settle();
  assert.deepEqual(
    page.rows.value.map((row) => row.id),
    [499],
  );
  assert.equal(signedOut(), 0);
  assert.equal(page.error.value, '');
});

test('refresh invalidates old appends, deduplicates moved rows, and unmount aborts pending work', async (t) => {
  const { page, requests, unmount, signedOut } = harness(t);
  requests[0].resolve(response('hosts', 0, 10));
  await settle();
  const old = page.loadMore();
  const refresh = page.load();
  assert.equal(requests[1].signal.aborted, true);
  requests[2].resolve(response('hosts', 100, 10));
  await refresh;
  requests[1].resolve(response('hosts', 10, 10));
  await old;
  assert.equal(page.rows.value[0].id, 100);
  const append = page.loadMore();
  requests[3].resolve(response('hosts', 105, 10));
  await append;
  assert.equal(page.rows.value.length, 15);
  const pending = page.loadMore();
  unmount();
  assert.equal(requests[4].signal.aborted, true);
  requests[4].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await pending;
  assert.equal(signedOut(), 0);
});

test('viewport height determines initial rows plus buffer and resizing drains a visible sentinel', async (t) => {
  const previous = globalThis.window;
  const listeners = {};
  const frames = [];
  globalThis.window = {
    innerHeight: 480,
    addEventListener: (name, fn) => {
      listeners[name] = fn;
    },
    removeEventListener: (name) => {
      delete listeners[name];
    },
    requestAnimationFrame: (fn) => frames.push(fn),
    cancelAnimationFrame() {},
  };
  t.after(() => {
    globalThis.window = previous;
  });
  const { page, requests, unmount } = harness(t);
  const small = Number(requests[0].params.get('limit'));
  requests[0].resolve(response('hosts', 0, small));
  await settle();
  page.sentinel.value = { getBoundingClientRect: () => ({ top: 1200 }) };
  await nextTick();
  frames.shift()();
  assert.equal(requests.length, 1);
  globalThis.window.innerHeight = 1440;
  listeners.resize();
  frames.shift()();
  assert.equal(requests.length, 2);
  const large = Number(requests[1].params.get('limit'));
  assert.ok(large > small * 2);
  requests[1].resolve(response('hosts', small, large));
  await settle();
  unmount();
  assert.deepEqual(Object.keys(listeners), []);
});

test('a timed-out request shows a retryable error instead of an endless spinner', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { page, requests } = harness(t);
  requests[0].signal.addEventListener('abort', () => requests[0].reject(new Error('Aborted')));
  t.mock.timers.tick(15000);
  await settle();
  assert.equal(page.loading.value, false);
  assert.match(page.error.value, /taking too long/);
  assert.equal(requests[0].signal.aborted, true);
});

function scrollingHarness(t, field = 'hosts') {
  const previous = globalThis.window;
  const listeners = {};
  const frames = [];
  const browser = {
    innerHeight: 1000,
    scrollY: 0,
    addEventListener: (name, callback) => {
      listeners[name] = callback;
    },
    removeEventListener: (name) => {
      delete listeners[name];
    },
    requestAnimationFrame: (callback) => frames.push(callback),
    cancelAnimationFrame() {},
  };
  globalThis.window = browser;
  const fixture = harness(t, field);
  t.after(() => {
    globalThis.window = previous;
  });
  fixture.page.table.value = {
    getBoundingClientRect: () => ({ top: 180 - browser.scrollY }),
  };
  fixture.page.sentinel.value = {
    getBoundingClientRect: () => ({
      top: 180 + fixture.page.rows.value.length * 64 - browser.scrollY,
    }),
  };
  async function paint() {
    await settle();
    while (frames.length) {
      frames.shift()();
      await settle();
    }
  }
  async function scrollTo(position) {
    browser.scrollY = position;
    listeners.scroll();
    await paint();
  }
  return { ...fixture, browser, paint, scrollTo };
}

test('the first downward pixel prefetches well before the end and pending scroll movement refills the reserve', async (t) => {
  const { page, requests, paint, scrollTo } = scrollingHarness(t);
  const initial = Number(requests[0].params.get('limit'));
  assert.ok(initial >= Math.ceil((1000 - 180) / 64) + Math.floor(1000 / 64));
  requests[0].resolve(response('hosts', 0, initial));
  await paint();
  assert.equal(requests.length, 1, 'stationary page should not download the collection');
  const bottom = page.sentinel.value.getBoundingClientRect().top;
  assert.ok(bottom > 2000, 'loaded rows extend a screen beyond the viewport');
  await scrollTo(1);
  assert.equal(requests.length, 2, 'first movement must start the next chunk');
  assert.equal(page.loading.value, true);
  await scrollTo(1000);
  await scrollTo(1500);
  assert.equal(requests.length, 2, 'movement during a request must not overlap it');
  let loaded = initial;
  let index = 1;
  while (index < requests.length) {
    const size = Number(requests[index].params.get('limit'));
    requests[index].resolve(response('hosts', loaded, size));
    loaded += size;
    index++;
    await paint();
  }
  assert.ok(page.sentinel.value.getBoundingClientRect().top > 4000);
  assert.equal(page.loading.value, false);
  assert.ok(loaded < 200, 'prefetch should stay bounded to the scrolling reserve');
  await scrollTo(1400);
  assert.equal(requests.length, index, 'upward scrolling should not extend the reserve');
});

for (const field of ['hosts', 'jobs', 'credentials', 'users', 'targets', 'keys']) {
  test(`${field}: scroll movement keeps a responsive 500-record list ahead of the viewport`, async (t) => {
    const { page, requests, browser, paint, scrollTo } = scrollingHarness(t, field);
    let answered = 0;
    let loaded = 0;
    async function servePending() {
      while (answered < requests.length) {
        const pending = requests[answered++];
        assert.equal(Number(pending.params.get('cursor') || 0), loaded);
        const size = Number(pending.params.get('limit'));
        pending.resolve(response(field, loaded, size));
        loaded = Math.min(500, loaded + size);
        await paint();
      }
    }
    await servePending();
    for (let position = 1; loaded < 500; position += 400) {
      await scrollTo(position);
      if (page.loading.value) {
        assert.ok(
          page.sentinel.value.getBoundingClientRect().top > browser.innerHeight,
          'a responsive prefetch exposed the loading footer',
        );
      }
      await servePending();
      assert.equal(page.loading.value, false);
      assert.equal(page.error.value, '');
      assert.ok(position < 500 * 64, 'scrolling did not reach the complete list');
    }
    assert.equal(page.rows.value.length, 500);
    assert.equal(new Set(page.rows.value.map((row) => row.id)).size, 500);
  });
}

test('an opportunistic prefetch failure preserves rows and does not retry on movement', async (t) => {
  const { page, requests, paint, scrollTo } = scrollingHarness(t);
  const size = Number(requests[0].params.get('limit'));
  requests[0].resolve(response('hosts', 0, size));
  await paint();
  await scrollTo(1);
  requests[1].reject(new Error('Server unavailable.'));
  await paint();
  await scrollTo(100);
  assert.equal(requests.length, 2);
  assert.equal(page.rows.value.length, size);
  assert.equal(page.error.value, 'Server unavailable.');
});

test('filter cancellation ignores a late response during the search debounce', async (t) => {
  const query = ref('');
  const { page, requests, signedOut } = harness(t, 'hosts', {
    filters: () => ({ q: query.value }),
  });
  query.value = 'replacement';
  await nextTick();
  requests[0].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await settle();
  assert.equal(signedOut(), 0);
  assert.equal(page.error.value, '');
  assert.equal(page.loading.value, true);
  assert.deepEqual(page.rows.value, []);
  const latest = page.load();
  requests[1].resolve(response('hosts', 499, 1));
  await latest;
  assert.equal(page.rows.value[0].id, 499);
});
