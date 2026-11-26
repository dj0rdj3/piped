<script setup>
import { ChevronDown, ShieldCheck, X } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import TerminalOutput from './TerminalOutput.vue';
import { api } from '../../lib/api.js';
import { formatTimestamp } from '../../lib/date-time.js';
import { useExecutionSession } from './useExecutionSession.js';
const props = defineProps({
  job: { type: Object, required: true },
  initialRun: { type: Object, default: null },
  initialRunId: { type: String, default: '' },
  initialHostId: { type: String, default: '' },
});
const emit = defineEmits(['close', 'signed-out', 'selection']);
const {
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
} = useExecutionSession(props, emit, api, window);
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
