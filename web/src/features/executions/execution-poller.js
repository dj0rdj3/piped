// Selection changes cancel both the idle timer and any older request. Only the
// current refresh may report errors or schedule another poll.
export function createExecutionPoller({ load, onError, onSettled, delay, timers = globalThis }) {
  let controller;
  let timer;
  let stopped = false;

  async function refresh(options = {}) {
    if (stopped) {
      return;
    }
    timers.clearTimeout(timer);
    controller?.abort();
    const current = new AbortController();
    controller = current;
    try {
      await load(options, current.signal);
    } catch (error) {
      if (!current.signal.aborted) {
        onError(error);
      }
    } finally {
      if (!current.signal.aborted) {
        onSettled();
        timer = timers.setTimeout(() => refresh(), delay());
      }
    }
  }

  return {
    refresh,
    stop() {
      stopped = true;
      timers.clearTimeout(timer);
      controller?.abort();
    },
  };
}
