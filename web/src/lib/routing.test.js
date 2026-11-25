import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  canonicalPath,
  isPageNavigation,
  routeForPath,
  safeReturnPath,
  sessionPath,
} from './routing.js';

test('workspace entry URLs select the correct page and canonical path', () => {
  for (const [path, page] of [
    ['/', 'jobs'],
    ['/users', 'users'],
    ['/notifications', 'notifications'],
    ['/notifications/', 'notifications'],
    ['/api-keys', 'api-keys'],
    ['/api-keys/', 'api-keys'],
    ['/users/', 'users'],
    ['/credentials', 'credentials'],
    ['/credentials/', 'credentials'],
    ['/login', 'login'],
    ['/login/', 'login'],
  ]) {
    assert.equal(routeForPath(path), page);
    assert.equal(canonicalPath(path), `/${page}`);
  }
  for (const path of [
    '/api/users',
    '/users/missing',
    '/credentials/new',
    '/unknown',
    '/assets/missing.js',
  ]) {
    assert.equal(routeForPath(path), null);
    assert.equal(canonicalPath(path), path);
  }
});

test('sign-in has its own URL and preserves safe workspace return destinations', () => {
  assert.equal(sessionPath({ pathname: '/credentials' }, false), '/login?next=%2Fcredentials');
  assert.equal(sessionPath({ pathname: '/api-keys' }, false), '/login?next=%2Fapi-keys');
  assert.equal(safeReturnPath('/api-keys/'), '/api-keys');
  assert.equal(sessionPath({ pathname: '/users' }, false), '/login?next=%2Fusers');
  assert.equal(sessionPath({ pathname: '/login' }, false), '/login');
  assert.equal(
    sessionPath({ pathname: '/login/', search: '?next=%2Fcredentials' }, false),
    '/login?next=%2Fcredentials',
  );
  assert.equal(
    sessionPath({ pathname: '/login', search: '?next=%2Fcredentials' }, true),
    '/credentials',
  );
  assert.equal(sessionPath({ pathname: '/login' }, true), '/jobs');
  assert.equal(sessionPath({ pathname: '/' }, true), '/jobs');
  assert.equal(sessionPath({ pathname: '/' }, false), '/login?next=%2Fjobs');
  const destination = '/credentials?filter=deploy#keys';
  const login = sessionPath(
    { pathname: '/credentials', search: '?filter=deploy', hash: '#keys' },
    false,
  );
  assert.equal(login, '/login?next=' + encodeURIComponent(destination));
  assert.equal(
    sessionPath({ pathname: '/login', search: login.slice('/login'.length) }, true),
    destination,
  );
  assert.equal(
    sessionPath({ pathname: '/users/', search: '?filter=admin' }, true),
    '/users?filter=admin',
  );
});

test('login return destinations cannot redirect outside the app or loop through login', () => {
  for (const value of [
    null,
    '',
    'https://untrusted.example/users',
    '//untrusted.example/users',
    '/\\untrusted.example/users',
    '/login',
    '/login?next=/login',
    '/api/users',
    '/unknown',
    '/users\n',
    '/users\u0085',
    'javascript:alert(1)',
  ]) {
    assert.equal(safeReturnPath(value), '/jobs');
    assert.equal(
      sessionPath({ pathname: '/login', search: '?next=' + encodeURIComponent(value ?? '') }, true),
      '/jobs',
    );
  }
  assert.equal(safeReturnPath('/credentials/'), '/credentials');
  assert.equal(safeReturnPath('/api-keys/'), '/api-keys');
  assert.equal(safeReturnPath('/'), '/jobs');
});

test('navigation preserves browser modified-click and new-tab behavior', () => {
  assert.equal(isPageNavigation({ button: 0 }), true);
  for (const event of [
    { button: 1 },
    { button: 2 },
    { button: 0, ctrlKey: true },
    { button: 0, metaKey: true },
    { button: 0, shiftKey: true },
    { button: 0, altKey: true },
    { button: 0, defaultPrevented: true },
  ]) {
    assert.equal(isPageNavigation(event), false);
  }
});

test('hosts direct entry survives login redirects and canonicalization', () => {
  assert.equal(routeForPath('/hosts'), 'hosts');
  assert.equal(routeForPath('/hosts/'), 'hosts');
  assert.equal(canonicalPath('/hosts/'), '/hosts');
  assert.equal(safeReturnPath('/hosts?tag=linux'), '/hosts?tag=linux');
  assert.equal(sessionPath({ pathname: '/hosts' }, false), '/login?next=%2Fhosts');
  assert.equal(sessionPath({ pathname: '/login', search: '?next=%2Fhosts' }, true), '/hosts');
});

test('jobs direct entry survives login redirects and canonicalization', () => {
  assert.equal(routeForPath('/jobs'), 'jobs');
  assert.equal(routeForPath('/jobs/'), 'jobs');
  assert.equal(canonicalPath('/jobs/'), '/jobs');
  assert.equal(safeReturnPath('/jobs?search=backup'), '/jobs?search=backup');
  assert.equal(sessionPath({ pathname: '/jobs' }, false), '/login?next=%2Fjobs');
  assert.equal(sessionPath({ pathname: '/login', search: '?next=%2Fjobs' }, true), '/jobs');
});

test('cancelled navigation preserves forms and Back/Forward history', async () => {
  const { createPageNavigation } = await import('./routing.js');
  const entries = [{ url: 'https://piped.invalid/jobs', state: null }];
  let index = 0;
  let changes = 0;
  let allow = true;
  const location = new URL(entries[0].url);
  function moveURL(url) {
    location.href = new URL(url, location.href).href;
  }
  const browser = {
    location,
    history: {
      get state() {
        return entries[index].state;
      },
      replaceState(state, _title, url) {
        moveURL(url);
        entries[index] = { url: location.href, state };
      },
      pushState(state, _title, url) {
        moveURL(url);
        entries.splice(++index);
        entries.push({ url: location.href, state });
      },
      go(delta) {
        index += delta;
        moveURL(entries[index].url);
        navigation.pop({ state: entries[index].state });
      },
    },
  };
  const navigation = createPageNavigation(
    browser,
    () => {
      changes++;
    },
    () => allow,
  );
  navigation.push('/hosts');
  navigation.push('/credentials');
  allow = false;
  assert.equal(navigation.push('/users'), false);
  assert.equal(location.pathname, '/credentials');
  browser.history.go(-2);
  assert.equal(location.pathname, '/credentials');
  assert.equal(index, 2);
  assert.equal(entries.length, 3);
  assert.equal(changes, 2);
  allow = true;
  browser.history.go(-1);
  assert.equal(location.pathname, '/hosts');
  assert.equal(changes, 3);
  allow = false;
  browser.history.go(1);
  assert.equal(location.pathname, '/hosts');
  assert.equal(index, 1);
  assert.equal(entries.length, 3);
  allow = true;
  browser.history.go(1);
  assert.equal(location.pathname, '/credentials');
  assert.equal(changes, 4);
});
