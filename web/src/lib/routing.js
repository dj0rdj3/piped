// Page URLs use the browser's history API without a router dependency.
export function routeForPath(pathname) {
  if (pathname === '/users' || pathname === '/users/') {
    return 'users';
  }
  if (pathname === '/' || pathname === '/jobs' || pathname === '/jobs/') {
    return 'jobs';
  }
  if (pathname === '/hosts' || pathname === '/hosts/') {
    return 'hosts';
  }
  if (pathname === '/credentials' || pathname === '/credentials/') {
    return 'credentials';
  }
  if (pathname === '/login' || pathname === '/login/') {
    return 'login';
  }
  return null;
}

// Return only to known app pages; never follow an external or login URL.
export function safeReturnPath(value) {
  if (
    typeof value !== 'string' ||
    !value.startsWith('/') ||
    value.startsWith('//') ||
    /[\\\p{Cc}]/u.test(value)
  ) {
    return '/jobs';
  }
  const url = new URL(value, 'https://piped.invalid');
  const route = routeForPath(url.pathname);
  if (!['users', 'credentials', 'hosts', 'jobs'].includes(route)) {
    return '/jobs';
  }
  return canonicalPath(url.pathname) + url.search + url.hash;
}

export function sessionPath({ pathname, search = '', hash = '' }, signedIn) {
  const path = canonicalPath(pathname);
  if (routeForPath(path) === 'login') {
    return signedIn
      ? safeReturnPath(new URLSearchParams(search).get('next'))
      : path + search + hash;
  }
  if (signedIn) {
    return path + search + hash;
  }
  const next = safeReturnPath(path + search + hash);
  return '/login?next=' + encodeURIComponent(next);
}

export function canonicalPath(pathname) {
  const route = routeForPath(pathname);
  return route ? `/${route}` : pathname;
}

export function isPageNavigation(event) {
  return (
    !event.defaultPrevented &&
    event.button === 0 &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.shiftKey &&
    !event.altKey
  );
}

// Keep a position on app-created entries so declining Back/Forward restores the
// existing entry and its form without destroying the forward history.
export function createPageNavigation(browser, onChange, canLeave) {
  const { history, location } = browser;
  let position = history.state?.pipedPosition ?? 0;
  let restoring = false;
  history.replaceState({ ...history.state, pipedPosition: position }, '', location.href);
  let currentURL = location.href;

  return {
    remember() {
      currentURL = location.href;
    },
    push(path) {
      if (!canLeave()) {
        return false;
      }
      if (path !== location.pathname + location.search + location.hash) {
        position++;
        history.pushState({ pipedPosition: position }, '', path);
      }
      onChange();
      currentURL = location.href;
      return true;
    },
    pop(event) {
      if (restoring) {
        restoring = false;
        return;
      }
      const target = event.state?.pipedPosition;
      if (!canLeave()) {
        if (Number.isInteger(target) && target !== position) {
          restoring = true;
          history.go(position - target);
        } else {
          // Entries created before this app session have no known position.
          history.pushState({ pipedPosition: position }, '', currentURL);
        }
        return;
      }
      position = target ?? position - 1;
      history.replaceState({ ...history.state, pipedPosition: position }, '', location.href);
      onChange();
      currentURL = location.href;
    },
  };
}
