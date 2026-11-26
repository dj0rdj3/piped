import { computed, onUnmounted, ref, watch } from 'vue';
import { usePageLoad } from '../../composables/usePageLoad.js';
import { formChanges } from '../../lib/unsaved-changes.js';
import { resolveJobHostIDs } from './job-targets.js';

export function useJobDraft(props, emit, api, window) {
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
          const result = await api(
            `/jobs/schedule-preview?cron=${encodeURIComponent(expression)}`,
            {
              signal: controller.signal,
            },
          );
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
  return {
    name,
    script,
    hostIDs,
    targetTags,
    excludedHostIDs,
    scheduleEnabled,
    cronSchedule,
    runAsSudo,
    executionMode,
    stopOnFailure,
    secretInputs,
    hosts,
    notificationTargets,
    notificationIDs,
    query,
    error,
    busy,
    preview,
    previewError,
    previewLoading,
    formDialog,
    dirty,
    title,
    hostTags,
    availableTags,
    matchingTargetTags,
    partiallySelectedTags,
    includedHostIDs,
    availableHosts,
    allVisibleHostsSelected,
    scriptBytes,
    canSave,
    loadHosts,
    loadingHosts,
    hostError,
    loadNotifications,
    loadingNotifications,
    notificationError,
    toggleTag,
    toggleVisibleHosts,
    toggleHost,
    moveHost,
    submit,
    deselectHosts,
  };
}
