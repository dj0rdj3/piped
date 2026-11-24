<script setup>
import { computed, ref, watch } from 'vue';
import { LoaderCircle, Pencil, Plus, RefreshCw, Search, Server, Trash2 } from '@lucide/vue';
import HostDialog from './HostDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { usePageLoad } from '../../composables/usePageLoad.js';

const props = defineProps({ requestedAction: { type: Object, default: null } });
const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const hosts = ref([]);
const query = ref('');
const tag = ref('');
const notice = ref('');
const dialog = ref(null);
watch(
  () => props.requestedAction,
  (action) => {
    if (action) {
      dialog.value = { mode: 'create', target: { name: action.name } };
    }
  },
  { immediate: true },
);
const tags = computed(() => [...new Set(hosts.value.flatMap((host) => host.tags))].sort());
const filtered = computed(() =>
  hosts.value.filter((host) => {
    const text = [host.name, host.address, host.username, host.credential_name, ...host.tags]
      .join(' ')
      .toLowerCase();
    return (
      text.includes(query.value.toLowerCase()) && (!tag.value || host.tags.includes(tag.value))
    );
  }),
);

const { load, loading, error } = usePageLoad(
  () => api('/hosts'),
  (result) => {
    hosts.value = result.hosts;
    if (!tags.value.includes(tag.value)) {
      tag.value = '';
    }
  },
  () => emit('signed-out'),
);

async function saved() {
  notice.value = { create: 'Host added.', edit: 'Host updated.', delete: 'Host removed.' }[
    dialog.value.mode
  ];
  dialog.value = null;
  await load();
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="Hosts"
      title-id="hosts-title"
      @open-navigation="emit('open-navigation')"
    >
      <AppButton
        variant="primary"
        @click="dialog = { mode: 'create' }"
      >
        <Plus :size="18" />
        Add host
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
      aria-labelledby="hosts-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">
          {{ hosts.length }} {{ hosts.length === 1 ? 'host' : 'hosts' }}
        </span>
        <div class="flex min-w-0 flex-wrap items-center gap-2 max-[560px]:w-full">
          <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
            <AppButton
              variant="icon"
              aria-label="Refresh hosts"
              title="Refresh hosts"
              :disabled="loading"
              @click="load"
            >
              <RefreshCw
                :size="17"
                :class="loading ? 'motion-safe:animate-spin' : ''"
              />
            </AppButton>
            <label
              for="host-tag-filter"
              class="sr-only"
            >
              Filter by tag
            </label>
            <select
              id="host-tag-filter"
              v-model="tag"
              class="max-w-full rounded-md border border-border-strong bg-surface px-3 py-2.5 text-sm text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring max-[560px]:flex-1"
            >
              <option value="">All tags</option>
              <option
                v-for="item in tags"
                :key="item"
                :value="item"
              >
                {{ item }}
              </option>
            </select>
          </div>
          <div
            class="relative flex min-w-0 items-center gap-2 rounded-md border border-border-strong px-3 py-2.5 text-muted focus-within:border-focus focus-within:ring-3 focus-within:ring-focus-ring max-[560px]:w-full"
          >
            <Search
              :size="17"
              class="shrink-0"
            />
            <label
              for="search-hosts"
              class="sr-only"
            >
              Search hosts
            </label>
            <input
              id="search-hosts"
              v-model="query"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
              type="search"
              placeholder="Search hosts&hellip;"
              autocomplete="off"
            />
          </div>
        </div>
      </div>
      <div class="relative overflow-x-auto">
        <table class="w-full min-w-[44rem] border-collapse text-left">
          <thead>
            <tr>
              <th
                v-for="heading in ['Name', 'Connection', 'Authentication', 'Tags', 'Sudo']"
                :key="heading"
                scope="col"
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
              >
                {{ heading }}
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
              v-for="host in filtered"
              :key="host.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex items-center gap-3">
                  <Server
                    :size="18"
                    class="shrink-0 text-muted"
                  />
                  <strong
                    class="max-w-48 truncate font-[550]"
                    :title="host.name"
                  >
                    {{ host.name }}
                  </strong>
                </div>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <span
                  class="block max-w-64 truncate font-mono text-xs"
                  :title="host.address"
                >
                  {{ host.address.includes(':') ? `[${host.address}]` : host.address }}:{{
                    host.port
                  }}
                </span>
                <span
                  class="mt-1 block max-w-48 truncate text-xs text-muted"
                  :title="host.username"
                >
                  {{ host.username }}
                </span>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                <span
                  class="block max-w-48 truncate"
                  :title="host.credential_name"
                >
                  {{ host.credential_id ? host.credential_name : 'Host password' }}
                </span>
                <span
                  v-if="host.credential_id"
                  class="mt-1 block text-xs"
                >
                  SSH key
                </span>
              </td>
              <td class="max-w-56 border-b border-border px-4 py-2.5 text-sm">
                <div
                  v-if="host.tags.length"
                  class="flex max-w-48 items-center gap-1.5 overflow-x-auto rounded focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-2"
                  tabindex="0"
                  :aria-label="`Tags for ${host.name}`"
                >
                  <span
                    v-for="item in host.tags"
                    :key="item"
                    class="max-w-40 shrink-0 truncate rounded border border-border bg-surface-muted px-1.5 py-0.5 text-xs text-muted"
                    :title="item"
                  >
                    {{ item }}
                  </span>
                </div>
                <span
                  v-else
                  class="text-muted"
                >
                  -
                </span>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm whitespace-nowrap text-muted">
                {{
                  host.username === 'root'
                    ? 'Root'
                    : host.run_as_sudo
                      ? host.has_sudo_password
                        ? 'Password'
                        : 'Passwordless'
                      : 'Not configured'
                }}
              </td>
              <td class="border-b border-border px-4 py-2.5">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    :aria-label="`Edit ${host.name}`"
                    title="Edit"
                    @click="dialog = { mode: 'edit', target: host }"
                  >
                    <Pencil :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Remove ${host.name}`"
                    title="Remove"
                    @click="dialog = { mode: 'delete', target: host }"
                  >
                    <Trash2 :size="17" />
                  </AppButton>
                </div>
              </td>
            </tr>
            <tr v-if="loading && !hosts.length">
              <td
                colspan="6"
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
                colspan="6"
                class="p-12 text-center text-sm text-muted"
              >
                {{ query || tag ? 'No matching hosts.' : 'No hosts. Add a host to get started.' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div
        v-if="query || tag"
        class="border-t border-border px-4 py-3 text-xs text-muted"
      >
        {{ filtered.length }} of {{ hosts.length }} hosts
      </div>
    </section>
    <HostDialog
      v-if="dialog"
      :key="`${dialog.mode}-${dialog.target?.id || ''}`"
      :mode="dialog.mode"
      :target="dialog.target"
      @close="dialog = null"
      @saved="saved"
      @auth-error="emit('auth-error')"
    />
  </main>
</template>
