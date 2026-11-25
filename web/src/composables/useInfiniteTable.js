import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue';

// The active controller identifies the only request allowed to update rows.
// Filters/refreshes abort older work; fast scrolls never overlap page requests.
export function useInfiniteTable(request, field, signedOut, { filters, applyResult } = {}) {
  const rows = ref([]);
  const loading = ref(false);
  const error = ref('');
  const total = ref(0);
  const matched = ref(0);
  const sentinel = ref(null);
  const table = ref(null);
  const nextCursor = ref('');
  let controller;
  let observer;
  let searchTimer;
  let frame;
  let mounted = false;
  let lastScrollY = 0;
  let prefetchBottom = 0;

  function buffer() {
    return Math.max(256, globalThis.window?.innerHeight || 800);
  }

  function pageSize() {
    const height = globalThis.window?.innerHeight || 800;
    const top = Math.max(0, table.value?.getBoundingClientRect().top ?? 180);
    return Math.min(200, Math.max(8, Math.ceil((height - top + buffer()) / 64)));
  }

  function onScroll() {
    const scrollY = Math.max(0, globalThis.window?.scrollY || 0);
    if (scrollY > lastScrollY) {
      // Start on the first downward movement and retain three screens ahead.
      // Remember the target while a request is pending, including jumps to End.
      const height = globalThis.window?.innerHeight || 800;
      prefetchBottom = Math.max(prefetchBottom, scrollY + height * 4);
      checkPosition();
    }
    lastScrollY = scrollY;
  }

  function checkPosition() {
    if (frame || !mounted) {
      return;
    }
    frame = globalThis.window?.requestAnimationFrame?.(() => {
      frame = undefined;
      const height = globalThis.window?.innerHeight || 800;
      const scrollY = Math.max(0, globalThis.window?.scrollY || 0);
      const target = Math.max(height + buffer(), prefetchBottom - scrollY);
      if (sentinel.value && sentinel.value.getBoundingClientRect().top <= target) {
        void loadMore();
      }
    });
  }

  async function fetchPage(reset) {
    if (!reset && (loading.value || error.value || !nextCursor.value)) {
      return;
    }
    if (reset) {
      controller?.abort();
      nextCursor.value = '';
      prefetchBottom = 0;
      lastScrollY = Math.max(0, globalThis.window?.scrollY || 0);
    }
    const currentController = new AbortController();
    controller = currentController;
    const timeout = globalThis.setTimeout(() => currentController.abort(), 15000);
    const cursor = reset ? '' : nextCursor.value;
    loading.value = true;
    error.value = '';
    try {
      const params = new URLSearchParams({ limit: String(pageSize()), ...filters?.() });
      if (cursor) {
        params.set('cursor', cursor);
      }
      const result = await request(params, currentController.signal);
      if (controller !== currentController || !mounted) {
        return;
      }
      const incoming = result[field];
      if (reset) {
        rows.value = incoming;
      } else {
        const existing = new Set(rows.value.map((row) => row.id));
        rows.value.push(...incoming.filter((row) => !existing.has(row.id)));
      }
      total.value = result.page?.total ?? rows.value.length;
      matched.value = result.page?.matched ?? rows.value.length;
      nextCursor.value = result.page?.next_cursor || '';
      applyResult?.(result);
    } catch (err) {
      if (controller !== currentController || !mounted) {
        return;
      }
      if (err.status === 401) {
        signedOut();
      } else {
        error.value = currentController.signal.aborted
          ? 'The server is taking too long. Please try again.'
          : err.message;
      }
    } finally {
      globalThis.clearTimeout(timeout);
      if (controller === currentController && mounted) {
        loading.value = false;
        await nextTick();
        checkPosition();
      }
    }
  }

  function load() {
    globalThis.clearTimeout(searchTimer);
    return fetchPage(true);
  }

  function loadMore() {
    return fetchPage(false);
  }

  function retry() {
    const append = rows.value.length > 0 && nextCursor.value;
    error.value = '';
    return append ? loadMore() : load();
  }

  if (filters) {
    watch(filters, () => {
      controller?.abort();
      controller = undefined;
      rows.value = [];
      nextCursor.value = '';
      prefetchBottom = 0;
      loading.value = true;
      error.value = '';
      globalThis.clearTimeout(searchTimer);
      searchTimer = globalThis.setTimeout(load, 200);
    });
  }

  watch(sentinel, (element) => {
    observer?.disconnect();
    if (element && globalThis.IntersectionObserver) {
      observer = new globalThis.IntersectionObserver(checkPosition, {
        rootMargin: `${buffer()}px 0px`,
      });
      observer.observe(element);
    }
    checkPosition();
  });

  onMounted(() => {
    mounted = true;
    globalThis.window?.addEventListener?.('scroll', onScroll, { passive: true });
    globalThis.window?.addEventListener?.('resize', checkPosition);
    void load();
  });
  onUnmounted(() => {
    mounted = false;
    controller?.abort();
    globalThis.clearTimeout(searchTimer);
    observer?.disconnect();
    globalThis.window?.cancelAnimationFrame?.(frame);
    globalThis.window?.removeEventListener?.('scroll', onScroll);
    globalThis.window?.removeEventListener?.('resize', checkPosition);
  });

  return {
    rows,
    loading,
    error,
    total,
    matched,
    table,
    sentinel,
    nextCursor,
    load,
    loadMore,
    retry,
  };
}
