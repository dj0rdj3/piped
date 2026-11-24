import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import { api, ApiError } from './api.js';

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
});

test('requests use same-origin cookies and the CSRF request header', async () => {
  globalThis.fetch = async (path, options) => {
    assert.equal(path, '/api/auth/login');
    assert.equal(options.credentials, 'same-origin');
    assert.equal(options.method, 'POST');
    assert.equal(options.headers['X-Piped-Request'], '1');
    assert.equal(options.headers['Content-Type'], 'application/json');
    assert.deepEqual(JSON.parse(options.body), { username: 'admin', password: 'secret' });
    return Response.json({ user: { id: 1 } });
  };
  assert.deepEqual(
    await api('/auth/login', { method: 'POST', body: { username: 'admin', password: 'secret' } }),
    { user: { id: 1 } },
  );
});

test('204 responses do not require a JSON body', async () => {
  globalThis.fetch = async () => new Response(null, { status: 204 });
  assert.equal(await api('/auth/logout', { method: 'POST' }), null);
});

test('request cancellation keeps its abort error and signal', async () => {
  const controller = new AbortController();
  globalThis.fetch = async (_path, { signal }) => {
    assert.equal(signal, controller.signal);
    controller.abort();
    signal.throwIfAborted();
  };
  await assert.rejects(
    api('/users', { signal: controller.signal }),
    (err) => err.name === 'AbortError',
  );
});

test('structured errors retain their HTTP status', async () => {
  globalThis.fetch = async () => Response.json({ error: 'Sign in required' }, { status: 401 });
  await assert.rejects(
    api('/auth/me'),
    (err) => err instanceof ApiError && err.status === 401 && err.message === 'Sign in required',
  );
});

test('proxy errors and connection failures have useful messages', async () => {
  globalThis.fetch = async () => new Response('Bad Gateway', { status: 502 });
  await assert.rejects(
    api('/users'),
    (err) => err.status === 502 && err.message.includes('try again'),
  );
  globalThis.fetch = async () => {
    throw new TypeError('internal network details');
  };
  await assert.rejects(
    api('/users'),
    (err) =>
      err.status === 0 &&
      err.message.includes('Could not reach') &&
      !err.message.includes('internal network details'),
  );
});
