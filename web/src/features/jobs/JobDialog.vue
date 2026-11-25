<script setup>
import { computed, onUnmounted, ref, watch } from 'vue';
import { ArrowDown, ArrowUp, LoaderCircle, Search, Terminal, Trash2 } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import FormInput from '../../components/ui/FormInput.vue';
import NamedSecretsEditor from '../../components/ui/NamedSecretsEditor.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { usePageLoad } from '../../composables/usePageLoad.js';
import { formChanges } from '../../lib/unsaved-changes.js';
import { formatScheduleTime } from '../../lib/date-time.js';
import { resolveJobHostIDs } from './job-targets.js';

const props = defineProps({
  serverTimezone: { type: String, default: 'server local time' },
  mode: { type: String, required: true },
  target: { type: Object, default: null },
});
const emit = defineEmits(['close', 'saved', 'auth-error']);
const name = ref(
  props.mode === 'duplicate'
    ? `${Array.from(props.target.name).slice(0, 93).join('')} (copy)`
    : props.target?.name || '',
);
const script = ref(props.target?.script || '');
const hostIDs = ref([...(props.target?.host_ids || [])]);
const targetTags = ref([...(props.target?.host_tags || [])]);
const excludedHostIDs = ref([...(props.target?.excluded_host_ids || [])]);
const scheduleEnabled = ref(
  props.mode === 'duplicate' ? false : (props.target?.schedule?.enabled ?? false),
);
const cronSchedule = ref(props.target?.schedule?.cron || '');
const runAsSudo = ref(props.target?.run_as_sudo ?? false);
const executionMode = ref(props.target?.execution_mode || 'sequential');
const stopOnFailure = ref(props.target?.stop_on_failure ?? true);
const secretInputs = ref(
  (props.target?.secret_names || []).map((name, key) => ({ key, name, value: '', stored: true })),
);
const hosts = ref([]);
const notificationTargets = ref([]);
const notificationIDs = ref([...(props.target?.notification_target_ids || [])]);
const initialNotificationIDs = ref([...notificationIDs.value]);
const query = ref('');
const error = ref('');
const busy = ref(false);
const preview = ref([]);
const previewError = ref('');
const previewLoading = ref(false);
const formDialog = ref(null);
const dirty = formChanges(
  () => [
    name.value,
    script.value,
    hostIDs.value,
    targetTags.value,
    excludedHostIDs.value,
    JSON.stringify(notificationIDs.value) === JSON.stringify(initialNotificationIDs.value)
      ? null
      : notificationIDs.value,
    scheduleEnabled.value,
    cronSchedule.value,
    runAsSudo.value,
    executionMode.value,
    stopOnFailure.value,
    secretInputs.value.map(({ name, value, stored }) => ({ name, value, stored })),
  ],
  () => props.mode !== 'delete',
);
let mounted = true;
const title = computed(
  () =>
    ({ create: 'Add job', duplicate: 'Duplicate job', edit: 'Edit job', delete: 'Remove job' })[
      props.mode
    ],
);
watch(
  cronSchedule,
  (expression, _previous, onCleanup) => {
    preview.value = [];
    previewError.value = '';
    previewLoading.value = props.mode !== 'delete' && !!expression.trim();
    if (!previewLoading.value) {
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      try {
        const result = await api(`/jobs/schedule-preview?cron=${encodeURIComponent(expression)}`, {
          signal: controller.signal,
        });
        if (!controller.signal.aborted) {
          preview.value = result.occurrences;
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          previewError.value = err.message;
          if (err.status === 401) {
            emit('auth-error');
          }
        }
      } finally {
        if (!controller.signal.aborted) {
          previewLoading.value = false;
        }
      }
    }, 300);
    onCleanup(() => {
      window.clearTimeout(timer);
      controller.abort();
    });
  },
  { immediate: true },
);
const hostTags = computed(() =>
  [...new Set([...hosts.value.flatMap((host) => host.tags), ...targetTags.value])].sort(),
);
const availableTags = computed(() =>
  hostTags.value.filter((tag) => tag.toLowerCase().includes(query.value.toLowerCase())),
);
const matchingTargetTags = computed(
  () =>
    new Map(
      hosts.value.map((host) => [
        host.id,
        targetTags.value.filter((tag) => host.tags.includes(tag)),
      ]),
    ),
);
const partiallySelectedTags = computed(
  () =>
    new Set(
      hosts.value
        .filter((host) => excludedHostIDs.value.includes(host.id))
        .flatMap((host) => matchingTargetTags.value.get(host.id)),
    ),
);
const includedHostIDs = computed(() =>
  resolveJobHostIDs(
    {
      host_ids: hostIDs.value,
      host_tags: targetTags.value,
      excluded_host_ids: excludedHostIDs.value,
    },
    hosts.value,
  ),
);
const orderedHosts = computed(() => {
  const selectedPositions = new Map(hostIDs.value.map((id, index) => [id, index]));
  return [...hosts.value].sort(
    (first, second) =>
      (selectedPositions.get(first.id) ?? hostIDs.value.length) -
      (selectedPositions.get(second.id) ?? hostIDs.value.length),
  );
});
const availableHosts = computed(() => {
  const search = query.value.toLowerCase();
  return orderedHosts.value.filter((host) =>
    [host.name, host.address, ...host.tags].join(' ').toLowerCase().includes(search),
  );
});
const allVisibleHostsSelected = computed(
  () =>
    availableHosts.value.length > 0 &&
    availableHosts.value.every((host) => includedHostIDs.value.has(host.id)),
);
const scriptBytes = computed(() => new window.TextEncoder().encode(script.value).length);
const canSave = computed(
  () =>
    !loadingHosts.value &&
    !hostError.value &&
    !loadingNotifications.value &&
    !notificationError.value &&
    (hostIDs.value.length > 0 || targetTags.value.length > 0) &&
    scriptBytes.value <= 65536,
);

onUnmounted(() => {
  mounted = false;
  for (const secret of secretInputs.value) {
    secret.value = '';
  }
});

const {
  load: loadHosts,
  loading: loadingHosts,
  error: hostError,
} = usePageLoad(
  () => api('/hosts'),
  (result) => {
    hosts.value = result.hosts;
  },
  () => emit('auth-error'),
  { loadOnMount: props.mode !== 'delete' },
);

const {
  load: loadNotifications,
  loading: loadingNotifications,
  error: notificationError,
} = usePageLoad(
  () => api('/notifications'),
  (result) => {
    notificationTargets.value = result.targets;
    if (props.mode === 'create') {
      notificationIDs.value = result.targets
        .filter((target) => target.enabled && target.default_enabled)
        .map((target) => target.id);
      initialNotificationIDs.value = [...notificationIDs.value];
    }
  },
  () => emit('auth-error'),
  { loadOnMount: props.mode !== 'delete' },
);

function toggleTag(tag) {
  if (targetTags.value.includes(tag) && !partiallySelectedTags.value.has(tag)) {
    targetTags.value = targetTags.value.filter((selectedTag) => selectedTag !== tag);
    return;
  }
  const members = new Set(
    hosts.value.filter((host) => host.tags.includes(tag)).map((host) => host.id),
  );
  excludedHostIDs.value = excludedHostIDs.value.filter((id) => !members.has(id));
  if (!targetTags.value.includes(tag)) {
    targetTags.value = [...targetTags.value, tag];
  }
}

function deselectHosts(ids) {
  hostIDs.value = hostIDs.value.filter((id) => !ids.has(id));
  const excluded = new Set(excludedHostIDs.value);
  for (const id of ids) {
    if (matchingTargetTags.value.get(id).length) {
      excluded.add(id);
    }
  }
  const deselectedTags = new Set([...ids].flatMap((id) => matchingTargetTags.value.get(id)));
  // An affected tag stays selected while any of its members remains included.
  for (const host of hosts.value) {
    if (!excluded.has(host.id)) {
      for (const tag of host.tags) {
        deselectedTags.delete(tag);
      }
    }
  }
  const remainingTags = targetTags.value.filter((tag) => !deselectedTags.has(tag));
  const clearedExclusions = new Set(
    hosts.value
      .filter(
        (host) =>
          host.tags.some((tag) => deselectedTags.has(tag)) &&
          !host.tags.some((tag) => remainingTags.includes(tag)),
      )
      .map((host) => host.id),
  );
  targetTags.value = remainingTags;
  excludedHostIDs.value = [...excluded].filter((id) => !clearedExclusions.has(id));
}

function toggleVisibleHosts() {
  if (allVisibleHostsSelected.value) {
    if (!query.value) {
      hostIDs.value = [];
      targetTags.value = [];
      excludedHostIDs.value = [];
      return;
    }
    deselectHosts(new Set(availableHosts.value.map((host) => host.id)));
    return;
  }
  const additionalIDs = availableHosts.value
    .filter(
      (host) => !hostIDs.value.includes(host.id) && !matchingTargetTags.value.get(host.id).length,
    )
    .map((host) => host.id);
  const visibleIDs = new Set(availableHosts.value.map((host) => host.id));
  excludedHostIDs.value = excludedHostIDs.value.filter((id) => !visibleIDs.has(id));
  hostIDs.value = [...hostIDs.value, ...additionalIDs];
}

function toggleHost(id) {
  if (includedHostIDs.value.has(id)) {
    deselectHosts(new Set([id]));
    return;
  }
  excludedHostIDs.value = excludedHostIDs.value.filter((excludedID) => excludedID !== id);
  if (!matchingTargetTags.value.get(id).length) {
    hostIDs.value = [...hostIDs.value, id];
  }
}

function moveHost(id, direction) {
  const index = hostIDs.value.indexOf(id);
  const next = index + direction;
  if (index < 0 || next < 0 || next >= hostIDs.value.length) {
    return;
  }
  const reordered = [...hostIDs.value];
  [reordered[index], reordered[next]] = [reordered[next], reordered[index]];
  hostIDs.value = reordered;
}

async function submit() {
  if (busy.value || (props.mode !== 'delete' && !canSave.value)) {
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    const body = { version: props.mode === 'duplicate' ? 0 : props.target?.version || 0 };
    if (props.mode === 'duplicate') {
      body.copy_from = { id: props.target.id, version: props.target.version };
    }
    if (props.mode !== 'delete') {
      Object.assign(body, {
        secrets: secretInputs.value.map((secret) => ({
          name: secret.name,
          ...(secret.value || !secret.stored ? { value: secret.value } : {}),
        })),
        name: name.value,
        script: script.value,
        host_ids: hostIDs.value,
        host_tags: targetTags.value,
        excluded_host_ids: excludedHostIDs.value,
        notification_target_ids: notificationIDs.value,
        execution_mode: executionMode.value,
        run_as_sudo: runAsSudo.value,
        schedule: { cron: cronSchedule.value, enabled: scheduleEnabled.value },
        stop_on_failure: stopOnFailure.value,
      });
    }
    const path = ['create', 'duplicate'].includes(props.mode)
      ? '/jobs'
      : `/jobs/${props.target.id}`;
    await api(path, {
      method: { create: 'POST', duplicate: 'POST', edit: 'PUT', delete: 'DELETE' }[props.mode],
      body,
    });
    if (mounted) {
      emit('saved');
    }
  } catch (err) {
    if (mounted) {
      error.value = err.message;
      if (err.status === 401) {
        emit('auth-error');
      }
    }
  } finally {
    for (const secret of secretInputs.value) {
      secret.value = '';
    }
    busy.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :dirty="dirty"
    :title="title"
    title-id="job-dialog-title"
    :busy="busy"
    :wide="mode !== 'delete'"
    @close="emit('close')"
  >
    <template #icon>
      <Trash2
        v-if="mode === 'delete'"
        :size="21"
      />
      <Terminal
        v-else
        :size="21"
      />
    </template>
    <form @submit.prevent="submit">
      <p
        v-if="mode === 'delete'"
        class="text-sm leading-relaxed text-muted"
      >
        Permanently remove
        <strong class="[overflow-wrap:anywhere]">{{ target.name }}</strong>
        and its script?
      </p>
      <fieldset
        v-else
        :disabled="busy"
        class="min-w-0 border-0 p-0"
      >
        <div class="my-4">
          <label
            for="job-name"
            class="mb-2 block text-sm font-[550]"
          >
            Name
          </label>
          <FormInput
            id="job-name"
            v-model="name"
            required
            maxlength="100"
            autocomplete="off"
            autofocus
          />
        </div>
        <div class="my-4">
          <label
            for="job-script"
            class="mb-2 block text-sm font-[550]"
          >
            Shell script
          </label>
          <textarea
            id="job-script"
            v-model="script"
            required
            maxlength="65536"
            spellcheck="false"
            autocapitalize="off"
            autocomplete="off"
            wrap="off"
            :aria-invalid="scriptBytes > 65536 ? 'true' : undefined"
            :aria-describedby="scriptBytes > 65536 ? 'job-script-limit' : undefined"
            class="block min-h-60 w-full resize-y overflow-x-auto rounded-lg border border-border-strong bg-surface-muted p-3 font-mono text-sm leading-6 text-foreground placeholder:text-muted focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
            placeholder="#!/bin/sh"
          ></textarea>
          <p
            v-if="scriptBytes > 65536"
            id="job-script-limit"
            class="mt-1.5 text-xs text-error"
          >
            Script must be 64 KiB or smaller.
          </p>
        </div>
        <fieldset class="my-4 min-w-0 border-0 p-0">
          <legend
            class="mb-2 flex w-full items-center gap-3 px-0 text-sm font-[550] after:h-px after:flex-1 after:bg-border"
          >
            Schedule
          </legend>
          <label class="inline-flex items-center gap-2 text-sm">
            <input
              v-model="scheduleEnabled"
              type="checkbox"
              class="size-4 shrink-0 accent-accent"
            />
            Run on schedule
          </label>
          <div class="mt-3">
            <label
              for="job-cron"
              class="mb-2 block text-sm font-[550]"
            >
              Cron expression
            </label>
            <FormInput
              id="job-cron"
              v-model="cronSchedule"
              :required="scheduleEnabled"
              maxlength="256"
              autocomplete="off"
              spellcheck="false"
              placeholder="0 2 * * *"
              aria-describedby="job-cron-help"
            />
            <p
              id="job-cron-help"
              class="mt-1.5 font-mono text-xs text-muted"
            >
              minute hour day-of-month month weekday
            </p>
          </div>
          <p class="mt-2 text-xs text-muted">
            Server timezone: {{ serverTimezone }}. Overlapping runs and missed schedules are
            skipped.
          </p>
          <div
            v-if="cronSchedule.trim()"
            class="mt-3 text-xs text-muted"
            aria-live="polite"
          >
            <p v-if="previewLoading">Calculating schedule&hellip;</p>
            <p
              v-else-if="previewError"
              class="text-error"
            >
              {{ previewError }}
            </p>
            <template v-else>
              <p>Schedule preview</p>
              <ul
                v-if="preview.length"
                class="mt-1 space-y-1 font-mono"
              >
                <li
                  v-for="time in preview"
                  :key="time"
                >
                  {{ formatScheduleTime(time) }}
                </li>
              </ul>
              <p
                v-else
                class="mt-1"
              >
                No matching times in the next 16 years.
              </p>
            </template>
          </div>
        </fieldset>
        <fieldset class="my-4 min-w-0 border-0 p-0">
          <legend
            class="mb-2 flex w-full items-center gap-3 px-0 text-sm font-[550] after:h-px after:flex-1 after:bg-border"
          >
            Notifications
          </legend>
          <StatusMessage
            v-if="notificationError"
            variant="error"
          >
            {{ notificationError }}
            <template #action>
              <AppButton
                variant="text"
                @click="loadNotifications"
              >
                Retry
              </AppButton>
            </template>
          </StatusMessage>
          <p
            v-else-if="loadingNotifications"
            class="py-3 text-sm text-muted"
            role="status"
          >
            Loading notification targets&hellip;
          </p>
          <p
            v-else-if="!notificationTargets.length"
            class="py-3 text-sm text-muted"
          >
            Add a target on the Notifications page.
          </p>
          <div
            v-else
            class="max-h-40 overflow-y-auto rounded-lg border border-border"
          >
            <label
              v-for="notification in notificationTargets"
              :key="notification.id"
              class="flex min-w-0 cursor-pointer items-center gap-3 border-b border-border px-3 py-2 text-sm last:border-0 hover:bg-surface-hover"
            >
              <input
                v-model="notificationIDs"
                type="checkbox"
                :value="notification.id"
                class="size-4 shrink-0 accent-accent"
              />
              <span class="min-w-0 flex-1 truncate">{{ notification.name }}</span>
              <span
                v-if="!notification.enabled"
                class="text-xs text-muted"
              >
                Disabled
              </span>
            </label>
          </div>
        </fieldset>
        <NamedSecretsEditor
          v-model="secretInputs"
          id-prefix="job-secret"
          name-pattern="[A-Z_][A-Z0-9_]*"
          name-placeholder="API_TOKEN"
          description="Injected as environment variables. Values cannot be viewed after saving."
        />
        <p
          v-if="mode === 'duplicate' && target.secret_names?.length"
          class="-mt-2 mb-4 text-xs text-muted"
        >
          Existing secret values will be copied securely when you save.
        </p>
        <fieldset class="my-4 min-w-0 border-0 p-0">
          <legend
            class="mb-2 flex w-full items-center gap-3 px-0 text-sm font-[550] after:h-px after:flex-1 after:bg-border"
          >
            Hosts ({{ includedHostIDs.size }} selected)
          </legend>
          <StatusMessage
            v-if="hostError"
            variant="error"
          >
            {{ hostError }}
            <template #action>
              <AppButton
                variant="text"
                @click="loadHosts"
              >
                Retry
              </AppButton>
            </template>
          </StatusMessage>
          <p
            v-else-if="loadingHosts"
            class="py-3 text-sm text-muted"
            role="status"
          >
            Loading hosts&hellip;
          </p>
          <p
            v-else-if="!hosts.length && !hostTags.length"
            class="py-3 text-sm text-muted"
          >
            No hosts currently available.
          </p>
          <template v-else>
            <div class="mb-2 flex min-w-0 items-center gap-2">
              <div class="relative min-w-0 flex-1">
                <Search
                  :size="16"
                  class="pointer-events-none absolute top-3 left-3 text-muted"
                />
                <input
                  v-model="query"
                  type="search"
                  aria-label="Find hosts or tags"
                  placeholder="Find hosts or tags"
                  class="w-full rounded-md border border-border-strong bg-surface py-2 pl-9 pr-3 text-sm focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
                />
              </div>
              <AppButton
                :disabled="!availableHosts.length"
                @click="toggleVisibleHosts"
              >
                {{ allVisibleHostsSelected ? 'Deselect all' : 'Select all' }}
              </AppButton>
            </div>
            <div class="max-h-64 overflow-y-auto rounded-lg border border-border">
              <label
                v-for="tag in availableTags"
                :key="`tag:${tag}`"
                class="flex min-w-0 cursor-pointer items-center gap-3 border-b border-border px-3 py-2 text-sm last:border-0 hover:bg-surface-hover"
                :class="{ 'bg-surface-hover': targetTags.includes(tag) }"
              >
                <input
                  type="checkbox"
                  :value="tag"
                  :checked="targetTags.includes(tag) && !partiallySelectedTags.has(tag)"
                  :indeterminate="partiallySelectedTags.has(tag)"
                  :aria-label="`Target tag ${tag}`"
                  class="size-4 shrink-0 accent-accent"
                  @change="toggleTag(tag)"
                />
                <span class="flex min-w-0 items-center gap-2">
                  <span class="truncate">{{ tag }}</span>
                  <span
                    class="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-[10px] leading-none text-muted"
                  >
                    tag
                  </span>
                </span>
              </label>
              <div
                v-for="host in availableHosts"
                :key="`host:${host.id}`"
                class="flex min-w-0 items-center gap-2 border-b border-border px-3 py-2 last:border-0 hover:bg-surface-hover"
                :class="{
                  'bg-surface-hover':
                    matchingTargetTags.get(host.id).length && includedHostIDs.has(host.id),
                }"
              >
                <label class="flex min-w-0 flex-1 cursor-pointer items-center gap-3">
                  <input
                    type="checkbox"
                    :value="host.id"
                    :aria-label="`Select ${host.name}`"
                    :checked="includedHostIDs.has(host.id)"
                    :aria-describedby="
                      matchingTargetTags.get(host.id).length || excludedHostIDs.includes(host.id)
                        ? `job-host-tags-${host.id}`
                        : undefined
                    "
                    class="size-4 shrink-0 accent-accent"
                    @change="toggleHost(host.id)"
                  />
                  <span
                    v-if="executionMode === 'sequential' && hostIDs.includes(host.id)"
                    class="shrink-0 text-xs text-muted"
                    :aria-label="`Execution order ${hostIDs.indexOf(host.id) + 1}`"
                  >
                    {{ hostIDs.indexOf(host.id) + 1 }}
                  </span>
                  <span class="min-w-0 flex-1">
                    <span class="flex min-w-0 items-center gap-2 text-sm">
                      <span class="truncate">{{ host.name }}</span>
                      <span
                        class="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-[10px] leading-none text-muted"
                      >
                        host
                      </span>
                    </span>
                    <span
                      v-if="
                        matchingTargetTags.get(host.id).length || excludedHostIDs.includes(host.id)
                      "
                      :id="`job-host-tags-${host.id}`"
                      class="mt-0.5 block truncate text-xs text-muted"
                      :title="matchingTargetTags.get(host.id).join(', ')"
                    >
                      {{ excludedHostIDs.includes(host.id) ? 'Excluded' : 'Included' }}
                      <template v-if="matchingTargetTags.get(host.id).length">
                        {{ excludedHostIDs.includes(host.id) ? 'from' : 'via' }}
                        {{ matchingTargetTags.get(host.id).length === 1 ? 'tag' : 'tags' }}:
                        {{ matchingTargetTags.get(host.id).join(', ') }}
                      </template>
                    </span>
                  </span>
                </label>
                <template v-if="hostIDs.includes(host.id) && executionMode === 'sequential'">
                  <AppButton
                    variant="icon"
                    :aria-label="`Move ${host.name} up`"
                    :disabled="hostIDs.indexOf(host.id) === 0"
                    @click="moveHost(host.id, -1)"
                  >
                    <ArrowUp :size="15" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Move ${host.name} down`"
                    :disabled="hostIDs.indexOf(host.id) === hostIDs.length - 1"
                    @click="moveHost(host.id, 1)"
                  >
                    <ArrowDown :size="15" />
                  </AppButton>
                </template>
              </div>
              <p
                v-if="!availableHosts.length && !availableTags.length"
                class="p-3 text-sm text-muted"
              >
                No matching hosts or tags.
              </p>
            </div>
          </template>
        </fieldset>
        <div class="my-4">
          <label
            for="job-execution-mode"
            class="mb-2 block text-sm font-[550]"
          >
            Execution mode
          </label>
          <select
            id="job-execution-mode"
            v-model="executionMode"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option value="sequential">Sequential</option>
            <option value="parallel">Parallel</option>
          </select>
        </div>
        <div class="my-4">
          <label class="inline-flex items-center gap-2 text-sm">
            <input
              v-model="runAsSudo"
              type="checkbox"
              class="size-4 shrink-0 accent-accent"
            />
            Use sudo
          </label>
          <p class="mt-1.5 text-xs text-muted">
            Uses each host's sudo configuration when enabled. Other hosts run as their SSH user.
            Root connections run directly. Scripts can include or omit sudo.
          </p>
        </div>
        <label class="my-4 flex items-start gap-2 text-sm">
          <input
            v-model="stopOnFailure"
            type="checkbox"
            class="mt-0.5 size-4 shrink-0 accent-accent"
          />
          Stop other hosts on failure or cancellation
        </label>
      </fieldset>
      <StatusMessage
        v-if="error"
        variant="error"
      >
        {{ error }}
      </StatusMessage>
      <div class="mt-5 flex justify-end gap-2.5">
        <AppButton
          :disabled="busy"
          @click="formDialog.requestClose()"
        >
          Cancel
        </AppButton>
        <AppButton
          type="submit"
          :variant="mode === 'delete' ? 'danger' : 'primary'"
          :disabled="busy || (mode !== 'delete' && !canSave)"
        >
          <LoaderCircle
            v-if="busy"
            :size="17"
            class="motion-safe:animate-spin"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else>{{ mode === 'delete' ? 'Remove job' : 'Save job' }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
