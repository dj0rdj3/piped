<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue';
import { ChevronDown, ShieldCheck, X } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import TerminalOutput from './TerminalOutput.vue';
import { api } from '../../lib/api.js';
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
import { formatTimestamp } from '../../lib/date-time.js';
import { createBackdropDismissal } from '../../lib/dialog-backdrop.js';

const props = defineProps({
  job: { type: Object, required: true },
  initialRun: { type: Object, default: null },
  initialRunId: { type: String, default: '' },
  initialHostId: { type: String, default: '' },
});
const emit = defineEmits(['close', 'signed-out', 'selection']);
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
  if (busy.value) {
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    await request(`/runs/${runID.value}/hosts/${targetHostID}/${path}`, { method: 'POST', body });
    return true;
  } catch (err) {
    error.value = err.message;
    if (err.status === 401) {
      emit('signed-out');
    }
    return false;
  } finally {
    busy.value = false;
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
</script>

<template>
  <dialog
    ref="dialog"
    aria-labelledby="execution-title"
    class="m-auto h-[calc(100dvh_-_2rem)] max-h-[calc(100dvh_-_2rem)] w-[calc(100%_-_2rem)] max-w-none overflow-hidden rounded-lg border border-border bg-surface p-0 text-foreground shadow-xl backdrop:bg-black/70 backdrop:backdrop-blur-[3px] sm:w-[calc(100%_-_12rem)] lg:w-[calc(100%_-_20rem)] dark:backdrop:bg-black/80"
    @cancel.self.prevent="emit('close')"
    @pointerdown="backdrop.pointerDown($event)"
    @pointercancel="backdrop.pointerCancel()"
    @click="backdrop.click($event)"
  >
    <div class="flex h-full min-h-0 flex-col">
      <div
        class="flex shrink-0 items-center justify-between gap-3 border-b border-border px-4 py-3"
      >
        <h2
          id="execution-title"
          class="min-w-0 truncate font-medium"
        >
          {{ job.name }} - Executions
        </h2>
        <AppButton
          variant="icon"
          aria-label="Close executions"
          @click="emit('close')"
        >
          <X :size="18" />
        </AppButton>
      </div>
      <div class="flex min-h-0 flex-1 flex-col overflow-y-auto p-4">
        <StatusMessage
          v-if="error"
          variant="error"
        >
          {{ error }}
        </StatusMessage>
        <p
          v-if="loading && !run"
          class="text-sm text-muted"
          role="status"
        >
          Loading executions&hellip;
        </p>
        <p
          v-else-if="!runs.length && !run"
          class="text-sm text-muted"
        >
          No executions yet.
        </p>
        <template v-else>
          <div class="mb-3 flex shrink-0 flex-wrap items-center gap-3">
            <label class="flex min-w-0 flex-1 basis-full items-center gap-2 text-sm sm:basis-0">
              Run
              <select
                v-model="runID"
                class="min-w-0 flex-1 rounded-md border border-border-strong bg-surface p-2 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
                @change="selectRun"
              >
                <option
                  v-for="item in runs"
                  :key="item.id"
                  :value="item.id"
                >
                  {{ formatTimestamp(item.started_at) }} -
                  {{ item.trigger === 'schedule' ? 'Scheduled' : 'Manual' }} -
                  {{ statusLabels[item.status] }}
                </option>
              </select>
            </label>
            <span class="text-xs text-muted">
              {{ run?.execution_mode === 'parallel' ? 'Parallel' : 'Sequential' }} -
              {{ run?.stop_on_failure ? 'Stop other hosts on failure' : 'Continue on failure' }}
              <span
                v-if="streamConnected && !run?.finished_at"
                class="ml-2 text-accent-text"
                role="status"
              >
                Live
              </span>
            </span>
          </div>
          <div
            class="mb-3 flex shrink-0 gap-2 overflow-x-auto pb-1"
            aria-label="Execution hosts"
          >
            <button
              v-for="target in run?.hosts || []"
              :key="target.host_id"
              class="shrink-0 rounded-md border px-3 py-2 text-left text-sm cursor-pointer focus-visible:outline-2 focus-visible:outline-focus"
              :class="
                hostID === target.host_id
                  ? 'border-focus bg-surface-muted'
                  : 'border-border hover:bg-surface-hover'
              "
              :aria-pressed="hostID === target.host_id"
              @click="
                hostID = target.host_id;
                selectHost();
              "
            >
              <span class="block font-medium">{{ target.name }}</span>
              <span class="text-xs text-muted">
                {{ statusLabels[target.status] }}
                <template v-if="target.exit_code !== null && target.exit_code !== undefined">
                  - Exit {{ target.exit_code }}
                </template>
              </span>
            </button>
          </div>
          <div
            v-if="pending || cancellable"
            class="mb-3 flex shrink-0 flex-wrap gap-2"
          >
            <AppButton
              v-if="pending"
              @click="approval = pending"
            >
              Review host key
            </AppButton>
            <AppButton
              v-if="cancellable"
              :disabled="busy"
              @click="action('cancel')"
            >
              Cancel host
            </AppButton>
          </div>
          <p
            v-if="host?.message && host.status === 'failed' && host.exit_code == null"
            class="mb-2 max-h-20 shrink-0 overflow-y-auto break-words text-sm text-muted"
          >
            {{ host.message }}
          </p>
          <div class="relative min-h-32 min-w-0 flex-1">
            <TerminalOutput
              :key="`${runID}/${hostID}`"
              ref="terminalComponent"
              :following="followingOutput"
              :text="
                output ||
                (outputLoading
                  ? 'Loading output...'
                  : run?.finished_at
                    ? 'No output.'
                    : 'Waiting for output...')
              "
              @scroll.passive="onTerminalScroll"
              @wheel.passive="$event.deltaY < 0 && pauseFollowing()"
              @pointerdown="pauseFollowing"
              @touchstart.passive="pauseFollowing"
              @keydown="onTerminalKey"
            />
            <button
              v-if="!followingOutput"
              type="button"
              aria-label="Jump to latest"
              title="Jump to latest"
              class="absolute right-3 bottom-3 flex size-9 cursor-pointer items-center justify-center rounded-md border border-neutral-600 bg-neutral-800 text-neutral-100 shadow-md hover:bg-neutral-700 focus-visible:outline-2 focus-visible:outline-focus focus-visible:outline-offset-2"
              @click="jumpToLatest"
            >
              <ChevronDown
                :size="20"
                aria-hidden="true"
              />
            </button>
          </div>
          <form
            v-if="host && !host.finished_at"
            class="mt-3 flex shrink-0 flex-wrap gap-2"
            @submit.prevent="send(input + '\n')"
          >
            <label
              for="terminal-input"
              class="sr-only"
            >
              Input to running script
            </label>
            <input
              id="terminal-input"
              v-model="input"
              :disabled="!interactive"
              autocomplete="off"
              spellcheck="false"
              maxlength="4000"
              placeholder="Input to running script"
              class="min-w-0 flex-1 basis-40 rounded-md border border-border-strong bg-surface px-3 py-2 font-mono text-sm outline-none focus:border-focus focus:ring-3 focus:ring-focus-ring"
              @keydown.ctrl.c.prevent="send('\u0003')"
              @keydown.ctrl.d.prevent="send('\u0004')"
            />
            <AppButton
              type="submit"
              :disabled="!interactive || inputBusy"
              variant="primary"
            >
              Send
            </AppButton>
            <AppButton
              :disabled="!interactive || inputBusy"
              @click="send('\u0003')"
            >
              Ctrl+C
            </AppButton>
            <AppButton
              :disabled="!interactive || inputBusy"
              @click="send('\u0004')"
            >
              Ctrl+D
            </AppButton>
          </form>
        </template>
      </div>
    </div>
    <AppDialog
      v-if="approval"
      title="Verify SSH host key"
      title-id="host-key-title"
      :busy="busy"
      @close="approval = null"
    >
      <template #icon><ShieldCheck :size="21" /></template>
      <p class="my-3 text-sm">{{ approval.address }}:{{ approval.port }}</p>
      <p
        v-if="approval.previous_fingerprint"
        class="mb-3 text-sm font-medium text-error"
      >
        The host key has changed. Verify it before accepting.
      </p>
      <p class="mb-1 text-xs text-muted">SHA256 fingerprint</p>
      <p class="mb-3 break-all font-mono text-sm">{{ approval.fingerprint }}</p>
      <template v-if="approval.previous_fingerprint">
        <p class="text-xs text-muted">Previously approved</p>
        <p class="mb-3 break-all font-mono text-xs">{{ approval.previous_fingerprint }}</p>
      </template>
      <pre
        class="max-h-32 overflow-auto rounded-md border border-border p-2 text-xs whitespace-pre-wrap break-all"
        >{{ approval.public_key }}</pre>
      <p class="mt-3 text-xs text-muted">Compare this key with a trusted source for the host.</p>
      <StatusMessage
        v-if="error"
        variant="error"
      >
        {{ error }}
      </StatusMessage>
      <div class="mt-5 flex justify-end gap-2.5">
        <AppButton
          :disabled="busy"
          @click="decide(false)"
        >
          Deny
        </AppButton>
        <AppButton
          variant="primary"
          :disabled="busy"
          @click="decide(true)"
        >
          Accept
        </AppButton>
      </div>
    </AppDialog>
  </dialog>
</template>
