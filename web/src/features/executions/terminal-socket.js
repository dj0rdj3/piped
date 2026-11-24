// Keep input/status independent of log traffic. The output connection grants
// another bounded page only after its previous page has been consumed locally.
// Reconnects resume persisted output; uncertain input is never retried.
export function createTerminalSocket({
  url,
  onOpen,
  onOutput,
  onState,
  onComplete,
  onClose,
  onAuthError,
  WebSocket = globalThis.WebSocket,
  timers = globalThis,
}) {
  const controlURL = new URL(url);
  controlURL.searchParams.set('channel', 'control');
  const outputURL = new URL(url);
  outputURL.searchParams.set('channel', 'output');
  const control = new WebSocket(controlURL.href);
  const output = new WebSocket(outputURL.href);
  const pending = new Map();
  let stopped = false;
  let opened = false;
  let outputComplete = false;
  let stateComplete = false;
  let completeSent = false;
  let sequence = 0;

  function stop() {
    if (stopped) {
      return;
    }
    stopped = true;
    for (const request of pending.values()) {
      timers.clearTimeout(request.timer);
      request.reject(
        new Error(
          request.kind === 'input'
            ? 'Input delivery was not confirmed. Check the terminal before sending again.'
            : 'History connection closed. Scroll up to retry.',
        ),
      );
    }
    pending.clear();
    control.close();
    output.close();
  }

  function maybeComplete() {
    if (
      !stopped &&
      !completeSent &&
      outputComplete &&
      stateComplete &&
      !Array.from(pending.values()).some((request) => request.kind === 'input')
    ) {
      completeSent = true;
      onComplete();
    }
  }

  for (const socket of [control, output]) {
    socket.onopen = () => {
      if (
        !stopped &&
        !opened &&
        control.readyState === WebSocket.OPEN &&
        output.readyState === WebSocket.OPEN
      ) {
        opened = true;
        onOpen();
      }
    };
    socket.onmessage = async ({ data }) => {
      if (stopped) {
        return;
      }
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        socket.close(1002, 'Invalid terminal response');
        return;
      }
      if (!message || typeof message !== 'object') {
        socket.close(1002, 'Invalid terminal response');
        return;
      }
      if (socket === output && message.type === 'output' && Array.isArray(message.chunks)) {
        const after = message.chunks.at(-1)?.id;
        if (!Number.isSafeInteger(after) || after < 1) {
          socket.close(1002, 'Invalid output cursor');
          return;
        }
        try {
          const consumed = onOutput(message.chunks);
          if (consumed?.then) {
            await consumed;
          }
          if (!stopped && output.readyState === WebSocket.OPEN) {
            output.send(JSON.stringify({ type: 'output_ack', id: ++sequence, after }));
          }
        } catch {
          socket.close(1011, 'Could not consume output');
        }
      } else if (
        socket === control &&
        message.type === 'state' &&
        message.run &&
        Array.isArray(message.host_keys)
      ) {
        stateComplete = !!message.run.finished_at;
        onState(message);
        maybeComplete();
      } else if (message.type === 'input_result' || message.type === 'history') {
        const request = pending.get(message.id);
        const kind = message.type === 'history' ? 'history' : 'input';
        if (request && request.kind === kind && request.socket === socket) {
          pending.delete(message.id);
          timers.clearTimeout(request.timer);
          if (message.error) {
            request.reject(new Error(message.error));
          } else {
            request.resolve(message);
          }
          maybeComplete();
        }
      } else if (socket === output && message.type === 'complete') {
        outputComplete = true;
        maybeComplete();
      }
    };
    socket.onerror = () => {};
    socket.onclose = ({ code }) => {
      if (stopped) {
        return;
      }
      stop();
      if (code === 4001) {
        onAuthError();
      } else {
        onClose();
      }
    };
  }

  function requestMessage(kind, body) {
    const socket = kind === 'input' ? control : output;
    if (stopped || socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('Terminal connection is not ready.'));
    }
    const id = ++sequence;
    return new Promise((resolve, reject) => {
      const timer = timers.setTimeout(
        () => {
          pending.delete(id);
          reject(
            new Error(
              kind === 'input'
                ? 'Input delivery was not confirmed. Check the terminal before sending again.'
                : 'History request timed out. Scroll up to retry.',
            ),
          );
          maybeComplete();
        },
        kind === 'input' ? 8000 : 15000,
      );
      pending.set(id, { resolve, reject, timer, kind, socket });
      try {
        socket.send(JSON.stringify({ type: kind, id, ...body }));
      } catch {
        pending.delete(id);
        timers.clearTimeout(timer);
        reject(
          new Error(
            kind === 'input'
              ? 'Could not send terminal input.'
              : 'Could not request terminal history.',
          ),
        );
      }
    });
  }

  return {
    get connected() {
      return (
        !stopped && control.readyState === WebSocket.OPEN && output.readyState === WebSocket.OPEN
      );
    },
    sendInput(data) {
      if (stopped || outputComplete || stateComplete || control.readyState !== WebSocket.OPEN) {
        return Promise.reject(new Error('Terminal connection is not ready.'));
      }
      const size = new globalThis.TextEncoder().encode(data).length;
      if (size === 0 || size > 4096) {
        return Promise.reject(new Error('Input must contain 1-4096 bytes.'));
      }
      return requestMessage('input', { data });
    },
    loadHistory(before, offset = 0) {
      if (
        !Number.isSafeInteger(before) ||
        before < 1 ||
        !Number.isSafeInteger(offset) ||
        offset < 0 ||
        offset > 4096
      ) {
        return Promise.reject(new Error('Invalid history cursor.'));
      }
      return requestMessage('history', { before, offset });
    },
    close: stop,
  };
}
