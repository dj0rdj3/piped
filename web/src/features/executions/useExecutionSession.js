import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue';
import { createExecutionPoller } from './execution-poller.js';
import {
  appendOutputChunks,
  createOutputBuffer,
  historyCursor,
  expandOutputHistory,
  prependOutputPage,
  readTerminalOutput,
} from './execution-output.js';
import { createTerminalSocket } from './terminal-socket.js';
import { createTerminalViewport } from './terminal-viewport.js';
import { createBackdropDismissal } from '../../lib/dialog-backdrop.js';

export function useExecutionSession(props, emit, api, window) {
  const runs = ref([]);
  const run = ref(props.initialRun);
  const runID = ref(props.initialRun?.id || props.initialRunId);
  const hostID = ref(props.initialHostId || props.initialRun?.hosts[0]?.host_id || '');
  const keys = ref([]);
  const approval = ref(null);
  const error = ref('');
  const loading = ref(true);
  const busy = ref(false);
  const inputBusy = ref(false);
  let inputRequest = 0;
  let actionRequest = 0;
  const input = ref('');
  const output = ref('');
  const terminalComponent = ref(null);
  const terminal = computed(() => terminalComponent.value?.element);
  const dialog = ref(null);
  const backdrop = createBackdropDismissal(() => emit('close'));
  const hasEarlierOutput = ref(false);
  const followingOutput = ref(true);
  const historyLoading = ref(false);
  let historyRequest = 0;
  let historyRequested = false;
  const buffers = new Map();
  const runDetails = new Map();
  const runHosts = new Map();
  const outputLoading = ref(false);
  const seenKeys = new Set();
  const controller = new AbortController();
  let mounted = true;
  let catchingUp = false;
  let stream = null;
  let streamKey = '';
  let reconnectTimer;
  let reconnectAttempts = 0;
  let outputFrame;
  const streamConnected = ref(false);
  const viewport = createTerminalViewport({
    element: () => terminal.value,
    nextTick,
    onFollowing: (following) => (followingOutput.value = following),
  });
  const host = computed(() => run.value?.hosts.find((item) => item.host_id === hostID.value));
  const interactive = computed(() => host.value?.status === 'running');
  const cancellable = computed(() =>
    ['queued', 'connecting', 'awaiting_approval', 'running'].includes(host.value?.status),
  );
  const pending = computed(() => keys.value.find((key) => key.host_id === hostID.value));
  const statusLabels = {
    queued: 'Queued',
    connecting: 'Connecting',
    awaiting_approval: 'Awaiting host key',
    running: 'Running',
    succeeded: 'Succeeded',
    failed: 'Failed',
    interrupted: 'Interrupted',
    cancelled: 'Cancelled',
    skipped: 'Skipped',
  };
  const poller = createExecutionPoller({
    load: poll,
    onError: reportPollError,
    onSettled() {
      loading.value = false;
      outputLoading.value = false;
    },
    delay: () => (stream ? 10000 : catchingUp ? 0 : run.value?.finished_at ? 3000 : 750),
  });
  onMounted(() => {
    dialog.value.showModal();
    void poller.refresh();
  });
  onUnmounted(() => {
    mounted = false;
    actionRequest++;
    input.value = '';
    stopStream();
    poller.stop();
    buffers.clear();
    runDetails.clear();
    runHosts.clear();
    void viewport.activate(null);
    controller.abort();
  });
  async function request(path, options = {}) {
    return api(path, { signal: controller.signal, ...options });
  }
  function selectRun() {
    run.value = runDetails.get(runID.value) || null;
    hostID.value = runHosts.get(runID.value) || run.value?.hosts[0]?.host_id || '';
    keys.value = [];
    approval.value = null;
    selectHost();
  }
  function selectHost() {
    actionRequest++;
    busy.value = false;
    stopStream();
    input.value = '';
    inputBusy.value = false;
    inputRequest++;
    error.value = '';
    catchingUp = false;
    const buffer = selectedBuffer();
    void viewport.activate(buffer);
    if (hostID.value) {
      runHosts.set(runID.value, hostID.value);
    }
    output.value = buffer?.history?.text ?? buffer?.text ?? '';
    hasEarlierOutput.value = buffer?.history?.hasEarlier ?? buffer?.hasEarlier ?? false;
    historyLoading.value = false;
    historyRequested = false;
    historyRequest++;
    outputLoading.value = !buffer?.loaded;
    emitSelection();
    void poller.refresh({ history: false });
  }
  function selectedBuffer() {
    if (!runID.value || !hostID.value) {
      return null;
    }
    const identity = `${runID.value}/${hostID.value}`;
    if (!buffers.has(identity)) {
      buffers.set(identity, createOutputBuffer());
    }
    return buffers.get(identity);
  }
  function emitSelection() {
    emit('selection', { runID: runID.value, hostID: hostID.value });
  }
  async function poll({ history = true }, signal) {
    catchingUp = false;
    // A known selection does not need to wait for the history-list request.
    const [listing, detail] = await Promise.all([
      history ? request(`/jobs/${props.job.id}/runs`, { signal }) : null,
      runID.value && !stream ? request(`/runs/${runID.value}`, { signal }) : null,
    ]);
    if (signal.aborted) {
      return;
    }
    if (listing) {
      runs.value = listing.runs;
    }
    if (!runID.value && runs.value.length) {
      runID.value = runs.value[0].id;
    }
    if (!runID.value) {
      return;
    }
    if (stream && streamKey === `${runID.value}/${hostID.value}`) {
      return;
    }
    const result = detail || (await request(`/runs/${runID.value}`, { signal }));
    if (signal.aborted) {
      return;
    }
    applyRunState(result);
    await readOutput(signal);
  }
  function applyRunState(result) {
    run.value = result.run;
    runDetails.set(runID.value, result.run);
    runs.value = runs.value.map((item) =>
      item.id === result.run.id ? { ...item, ...result.run } : item,
    );
    keys.value = result.host_keys;
    if (
      approval.value &&
      !keys.value.some(
        (key) =>
          key.host_id === approval.value.host_id && key.fingerprint === approval.value.fingerprint,
      )
    ) {
      approval.value = null;
    }
    if (!run.value.hosts.some((target) => target.host_id === hostID.value)) {
      hostID.value = run.value.hosts[0]?.host_id || '';
    }
    emitSelection();
    for (const key of keys.value) {
      const identity = `${runID.value}/${key.host_id}/${key.fingerprint}`;
      if (!seenKeys.has(identity) && !approval.value) {
        seenKeys.add(identity);
        approval.value = key;
      }
    }
  }
  function reportPollError(err) {
    if (!mounted) {
      return;
    }
    if (err.status === 404 && runID.value) {
      runID.value = runs.value.find((item) => item.id !== runID.value)?.id || '';
      // Let this refresh finish before starting the replacement selection.
      window.queueMicrotask(selectRun);
    } else {
      error.value = err.message;
    }
    if (err.status === 401) {
      emit('signed-out');
    }
  }
  async function readOutput(signal) {
    if (!hostID.value) {
      return;
    }
    const identity = `${runID.value}/${hostID.value}`;
    const buffer = selectedBuffer();
    void viewport.activate(buffer);
    if (stream && streamKey === identity) {
      return;
    }
    const more = await readTerminalOutput(
      buffer,
      `/runs/${runID.value}/hosts/${hostID.value}/output`,
      request,
      signal,
      () => displayOutput(buffer),
    );
    if (signal.aborted) {
      return;
    }
    catchingUp = more;
    if (!catchingUp && run.value.finished_at) {
      buffer.complete = true;
    }
    if (!catchingUp && !reconnectTimer && (!run.value.finished_at || buffer.hasEarlier)) {
      openStream(identity, buffer);
    }
  }
  async function displayOutput(buffer) {
    if (buffers.get(`${runID.value}/${hostID.value}`) !== buffer) {
      return;
    }
    output.value = buffer.history?.text ?? buffer.text;
    hasEarlierOutput.value = buffer.history?.hasEarlier ?? buffer.hasEarlier;
    outputLoading.value = false;
    await viewport.rendered(buffer);
  }
  function stopStream() {
    window.cancelAnimationFrame(outputFrame);
    outputFrame = undefined;
    window.clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
    reconnectAttempts = 0;
    stream?.close();
    stream = null;
    streamKey = '';
    streamConnected.value = false;
  }
  function openStream(identity, buffer) {
    const url = new URL(
      `/api/runs/${runID.value}/hosts/${hostID.value}/terminal`,
      window.location.origin,
    );
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    url.searchParams.set('after', buffer.after);
    const current = createTerminalSocket({
      url: url.href,
      onOpen() {
        if (stream === current) {
          streamConnected.value = true;
          reconnectAttempts = 0;
          if (historyRequested) {
            historyRequested = false;
            void loadOlderOutput();
          }
        }
      },
      onOutput(chunks) {
        if (stream === current) {
          appendOutputChunks(buffer, chunks);
          scheduleDisplay();
        }
      },
      onState(result) {
        if (stream === current) {
          applyRunState(result);
        }
      },
      onComplete() {
        if (stream === current) {
          flushDisplay();
          buffer.complete = true;
          if (!buffer.hasEarlier) {
            stopStream();
            void poller.refresh();
          }
        }
      },
      onClose() {
        if (stream !== current || !mounted) {
          return;
        }
        flushDisplay();
        stream = null;
        streamKey = '';
        streamConnected.value = false;
        const delay = Math.min(1000 * 2 ** reconnectAttempts++, 10000);
        reconnectTimer = window.setTimeout(() => {
          reconnectTimer = undefined;
          void poller.refresh({ history: false });
        }, delay);
      },
      onAuthError() {
        if (stream === current) {
          emit('signed-out');
        }
      },
    });
    streamKey = identity;
    stream = current;

    function scheduleDisplay() {
      // Paint at the next frame without delaying status or input acknowledgements.
      outputFrame ??= window.requestAnimationFrame(flushDisplay);
    }

    function flushDisplay() {
      window.cancelAnimationFrame(outputFrame);
      outputFrame = undefined;
      if (stream !== current || !mounted) {
        return;
      }
      void displayOutput(buffer);
    }
  }
  async function loadOlderOutput() {
    if (historyLoading.value || !hasEarlierOutput.value) {
      return;
    }
    const identity = `${runID.value}/${hostID.value}`;
    const buffer = buffers.get(identity);
    if (!buffer?.loaded) {
      return;
    }
    if (!stream?.connected) {
      historyRequested = true;
      return;
    }
    const cursor = historyCursor(buffer);
    if (!cursor) {
      return;
    }
    const currentRequest = ++historyRequest;
    const history = expandOutputHistory(buffer);
    viewport.pause();
    output.value = history.text;
    historyLoading.value = true;
    error.value = '';
    try {
      const page = await stream.loadHistory(cursor.before, cursor.offset);
      if (
        !mounted ||
        currentRequest !== historyRequest ||
        identity !== `${runID.value}/${hostID.value}`
      ) {
        return;
      }
      const previousHeight = terminal.value?.scrollHeight || 0;
      const previousTop = terminal.value?.scrollTop || 0;
      output.value = prependOutputPage(buffer, page);
      hasEarlierOutput.value = buffer.history.hasEarlier;
      await nextTick();
      if (mounted && currentRequest === historyRequest && terminal.value) {
        terminal.value.scrollTop = previousTop + terminal.value.scrollHeight - previousHeight;
      }
    } catch (err) {
      if (mounted && currentRequest === historyRequest) {
        error.value = err.message;
      }
    } finally {
      if (mounted && currentRequest === historyRequest) {
        historyLoading.value = false;
      }
    }
  }
  function onTerminalScroll() {
    if (!terminal.value) {
      return;
    }
    viewport.scroll();
    if (!followingOutput.value) {
      const buffer = buffers.get(`${runID.value}/${hostID.value}`);
      if (buffer?.loaded) {
        expandOutputHistory(buffer);
      }
    }
    if (terminal.value.scrollTop <= 80) {
      void loadOlderOutput();
    }
  }
  function pauseFollowing() {
    viewport.pause();
    const buffer = selectedBuffer();
    if (buffer?.loaded) {
      expandOutputHistory(buffer);
    }
  }
  function onTerminalKey(event) {
    if (['ArrowUp', 'PageUp', 'Home'].includes(event.key)) {
      pauseFollowing();
    }
  }
  async function jumpToLatest() {
    historyRequest++;
    historyRequested = false;
    historyLoading.value = false;
    const buffer = buffers.get(`${runID.value}/${hostID.value}`);
    if (!buffer) {
      return;
    }
    buffer.history = null;
    viewport.follow();
    output.value = buffer.text;
    hasEarlierOutput.value = buffer.hasEarlier;
    await viewport.rendered(buffer);
  }
  async function action(path, body, targetHostID = hostID.value) {
    if (busy.value || !mounted) {
      return false;
    }
    const currentRequest = ++actionRequest;
    busy.value = true;
    error.value = '';
    try {
      await request(`/runs/${runID.value}/hosts/${targetHostID}/${path}`, { method: 'POST', body });
      return mounted && currentRequest === actionRequest;
    } catch (err) {
      if (mounted && currentRequest === actionRequest) {
        error.value = err.message;
        if (err.status === 401) {
          emit('signed-out');
        }
      }
      return false;
    } finally {
      if (mounted && currentRequest === actionRequest) {
        busy.value = false;
      }
    }
  }

  async function send(data) {
    if (inputBusy.value || !interactive.value) {
      return;
    }
    const identity = `${runID.value}/${hostID.value}`;
    const currentRequest = ++inputRequest;
    const draft = input.value;
    inputBusy.value = true;
    error.value = '';
    try {
      if (stream?.connected) {
        await stream.sendInput(data);
      } else {
        await request(`/runs/${runID.value}/hosts/${hostID.value}/input`, {
          method: 'POST',
          body: { data },
        });
      }
      if (
        currentRequest === inputRequest &&
        identity === `${runID.value}/${hostID.value}` &&
        input.value === draft
      ) {
        input.value = '';
      }
    } catch (err) {
      if (
        mounted &&
        currentRequest === inputRequest &&
        identity === `${runID.value}/${hostID.value}`
      ) {
        error.value = err.message;
        if (err.status === 401) {
          emit('signed-out');
        }
      }
    } finally {
      if (currentRequest === inputRequest) {
        inputBusy.value = false;
      }
    }
  }
  async function decide(accept) {
    const key = approval.value;
    if (!key) {
      return;
    }
    const saved = await action('host-key', { fingerprint: key.fingerprint, accept }, key.host_id);
    if (saved && approval.value === key) {
      approval.value = null;
    }
  }
  return {
    runs,
    run,
    runID,
    hostID,
    approval,
    error,
    loading,
    busy,
    inputBusy,
    input,
    output,
    terminalComponent,
    terminal,
    dialog,
    backdrop,
    followingOutput,
    outputLoading,
    streamConnected,
    host,
    interactive,
    cancellable,
    pending,
    statusLabels,
    selectRun,
    selectHost,
    onTerminalScroll,
    pauseFollowing,
    onTerminalKey,
    jumpToLatest,
    action,
    send,
    decide,
    loadOlderOutput,
    applyRunState,
    readOutput,
    keys,
    historyLoading,
    hasEarlierOutput,
  };
}
