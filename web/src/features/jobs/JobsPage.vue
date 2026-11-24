<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import {
  Copy,
  History,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Search,
  Trash2,
} from '@lucide/vue';
import JobDialog from './JobDialog.vue';
import ExecutionDialog from '../executions/ExecutionDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { readExecutionView, writeExecutionView } from '../executions/execution-view.js';
import { formatTimestamp } from '../../lib/date-time.js';

const props = defineProps({
  requestedAction: { type: Object, default: null },
  currentUserId: { type: Number, required: true },
});
const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const jobs = ref([]);
const serverTimezone = ref('server local time');
const hosts = ref([]);
const query = ref('');
const loading = ref(false);
const error = ref('');
const notice = ref('');
const dialog = ref(null);
const execution = ref(null);
const starting = ref(false);
let requestID = 0;
let restored = false;
let summaryTimer;
watch(
  () => props.requestedAction,
  (action) => {
    if (action) {
      dialog.value = { mode: 'create', target: { name: action.name } };
    }
  },
  { immediate: true },
);
const hostNames = computed(() => new Map(hosts.value.map((host) => [host.id, host.name])));
const filtered = computed(() =>
  jobs.value.filter((job) =>
    [job.name, ...job.host_ids.map((id) => hostNames.value.get(id))]
      .join(' ')
      .toLowerCase()
      .includes(query.value.toLowerCase()),
  ),
);
onMounted(() => {
  load();
  summaryTimer = window.setInterval(refreshSummaries, 10000);
});
onUnmounted(() => {
  requestID++;
  window.clearInterval(summaryTimer);
});

async function refreshSummaries() {
  if (loading.value || document.visibilityState === 'hidden') {
    return;
  }
  const request = ++requestID;
  try {
    const result = await api('/jobs');
    if (request === requestID) {
      jobs.value = result.jobs;
    }
  } catch (err) {
    if (request === requestID && err.status === 401) {
      emit('signed-out');
    }
  }
}

async function load() {
  const request = ++requestID;
  loading.value = true;
  error.value = '';
  try {
    const [jobResult, hostResult] = await Promise.all([api('/jobs'), api('/hosts')]);
    if (request === requestID) {
      jobs.value = jobResult.jobs;
      serverTimezone.value = jobResult.server_timezone || 'server local time';
      hosts.value = hostResult.hosts;
      if (!restored) {
        restored = true;
        const view = readExecutionView(window, props.currentUserId);
        const job = jobs.value.find((item) => item.id === view?.jobID);
        if (job) {
          execution.value = { job, runID: view.runID, hostID: view.hostID };
        }
      }
    }
  } catch (err) {
    if (request !== requestID) {
      return;
    }
    if (err.status === 401) {
      emit('signed-out');
    } else {
      error.value = err.message;
    }
  } finally {
    if (request === requestID) {
      loading.value = false;
    }
  }
}
function rememberExecution(view) {
  writeExecutionView(window, props.currentUserId, {
    jobID: execution.value.job.id,
    ...view,
  });
}
function openExecution(job, runID) {
  execution.value = { job, runID };
  rememberExecution({ runID });
}
function closeExecution() {
  execution.value = null;
  writeExecutionView(window, props.currentUserId, null);
  void load();
}
async function start(job) {
  if (starting.value) {
    return;
  }
  starting.value = true;
  error.value = '';
  try {
    const result = await api(`/jobs/${job.id}/run`, {
      method: 'POST',
      body: { version: job.version },
    });
    execution.value = { job, run: result.run };
    job.latest_run = {
      id: result.run.id,
      status: result.run.status,
      started_at: result.run.started_at,
    };
    rememberExecution({ runID: result.run.id, hostID: result.run.hosts[0]?.host_id });
  } catch (err) {
    error.value = err.message;
    if (err.status === 401) {
      emit('signed-out');
    }
  } finally {
    starting.value = false;
  }
}
async function saved() {
  notice.value = {
    create: 'Job added.',
    duplicate: 'Job duplicated.',
    edit: 'Job updated.',
    delete: 'Job removed.',
  }[dialog.value.mode];
  dialog.value = null;
  await load();
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="Jobs"
      title-id="jobs-title"
      @open-navigation="emit('open-navigation')"
    >
      <AppButton
        variant="primary"
        @click="dialog = { mode: 'create' }"
      >
        <Plus :size="18" />
        Add job
      </AppButton>
    </PageHeading>
    <StatusMessage v-if="notice">{{ notice }}</StatusMessage>
    <StatusMessage
      v-if="error"
      variant="error"
    >
      {{ error }}
      <template #action>
        <AppButton
          variant="text"
          @click="load"
        >
          Retry
        </AppButton>
      </template>
    </StatusMessage>
    <section
      class="overflow-hidden rounded-[.65rem] border border-border bg-surface shadow-sm shadow-black/5 dark:shadow-black/20"
      aria-labelledby="jobs-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">
          {{ jobs.length }} {{ jobs.length === 1 ? 'job' : 'jobs' }}
        </span>
        <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
          <AppButton
            variant="icon"
            aria-label="Refresh jobs"
            title="Refresh jobs"
            :disabled="loading"
            @click="load"
          >
            <RefreshCw
              :size="17"
              :class="loading ? 'motion-safe:animate-spin' : ''"
            />
          </AppButton>
          <div
            class="relative flex min-w-0 items-center gap-2 rounded-md border border-border-strong px-3 py-2.5 text-muted focus-within:border-focus focus-within:ring-3 focus-within:ring-focus-ring max-[560px]:w-full"
          >
            <Search
              :size="17"
              class="shrink-0"
            />
            <label
              for="job-search"
              class="sr-only"
            >
              Search jobs
            </label>
            <input
              id="job-search"
              v-model="query"
              type="search"
              placeholder="Search jobs or hosts&hellip;"
              autocomplete="off"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
            />
          </div>
        </div>
      </div>
      <div class="relative overflow-x-auto">
        <table class="w-full min-w-[42rem] border-collapse text-left text-sm">
          <thead>
            <tr>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                Name
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                Hosts
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                Execution
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                On failure
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                Schedule
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                Latest run
              </th>
              <th
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody class="[&>tr:last-child>td]:border-b-0">
            <tr
              v-for="job in filtered"
              :key="job.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="max-w-64 border-b border-border px-4 py-2.5 text-sm">
                <button
                  class="block max-w-full truncate text-left font-[550] hover:underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus"
                  @click="dialog = { mode: 'edit', target: job }"
                >
                  {{ job.name }}
                </button>
              </td>
              <td class="max-w-64 border-b border-border px-4 py-2.5 text-sm">
                <span
                  v-if="job.host_ids.length === 1"
                  class="block truncate"
                  :title="hostNames.get(job.host_ids[0]) || 'Unavailable host'"
                >
                  {{ hostNames.get(job.host_ids[0]) || 'Unavailable host' }}
                </span>
                <span
                  v-else
                  class="text-muted"
                >
                  {{ job.host_ids.length }} hosts
                </span>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                {{ job.execution_mode === 'parallel' ? 'Parallel' : 'Sequential' }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                {{ job.stop_on_failure ? 'Stop other hosts' : 'Continue' }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                <span
                  v-if="job.schedule?.enabled"
                  class="whitespace-nowrap font-mono text-xs"
                  :title="serverTimezone"
                >
                  {{ job.schedule.cron }}
                </span>
                <span v-else>{{ job.schedule?.cron ? 'Paused' : 'Manual' }}</span>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <button
                  v-if="job.latest_run"
                  class="cursor-pointer text-left focus-visible:outline-3 focus-visible:outline-focus hover:underline"
                  :aria-label="`Open latest run for ${job.name}`"
                  @click="openExecution(job, job.latest_run.id)"
                >
                  <span
                    class="block capitalize"
                    :class="{ 'text-error': job.latest_run.status === 'failed' }"
                  >
                    {{ job.latest_run.status }}
                  </span>
                  <span class="block whitespace-nowrap text-xs text-muted">
                    {{ formatTimestamp(job.latest_run.started_at) }}
                  </span>
                </button>
                <span
                  v-else
                  class="text-muted"
                >
                  No runs
                </span>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    :disabled="starting"
                    :aria-label="`Run ${job.name}`"
                    title="Run job"
                    @click="start(job)"
                  >
                    <Play :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Executions for ${job.name}`"
                    title="Executions"
                    @click="openExecution(job)"
                  >
                    <History :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Edit ${job.name}`"
                    title="Edit"
                    @click="dialog = { mode: 'edit', target: job }"
                  >
                    <Pencil :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Duplicate ${job.name}`"
                    title="Duplicate"
                    @click="dialog = { mode: 'duplicate', target: job }"
                  >
                    <Copy :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Remove ${job.name}`"
                    title="Remove"
                    @click="dialog = { mode: 'delete', target: job }"
                  >
                    <Trash2 :size="17" />
                  </AppButton>
                </div>
              </td>
            </tr>
            <tr v-if="loading && !jobs.length">
              <td
                colspan="7"
                class="p-12 text-center text-sm text-muted"
              >
                <LoaderCircle
                  class="mr-2 inline align-middle motion-safe:animate-spin"
                  :size="20"
                />
                Loading&hellip;
              </td>
            </tr>
            <tr v-else-if="!filtered.length">
              <td
                colspan="7"
                class="p-12 text-center text-sm text-muted"
              >
                {{ query ? 'No matching jobs.' : 'No jobs. Add a job to get started.' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div
        v-if="query"
        class="border-t border-border px-4 py-3 text-xs text-muted"
      >
        {{ filtered.length }} of {{ jobs.length }} jobs
      </div>
    </section>
    <ExecutionDialog
      v-if="execution"
      :key="execution.run?.id || execution.job.id"
      :job="execution.job"
      :initial-run="execution.run"
      :initial-run-id="execution.runID"
      :initial-host-id="execution.hostID"
      @selection="rememberExecution"
      @close="closeExecution"
      @signed-out="emit('signed-out')"
    />
    <JobDialog
      v-if="dialog"
      :key="`${dialog.mode}-${dialog.target?.id || 'new'}`"
      :server-timezone="serverTimezone"
      :mode="dialog.mode"
      :target="dialog.target"
      @close="dialog = null"
      @saved="saved"
      @auth-error="emit('auth-error')"
    />
  </main>
</template>
