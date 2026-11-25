<script setup>
import { ref } from 'vue';
import { KeyRound, Plus, RefreshCw, Search, Trash2 } from '@lucide/vue';
import APIKeyDialog from './APIKeyDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { useInfiniteTable } from '../../composables/useInfiniteTable.js';
import TableLoadState from '../../components/ui/TableLoadState.vue';
import { formatTimestamp } from '../../lib/date-time.js';

const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const scopes = ref([]);
const query = ref('');

const notice = ref('');
const dialog = ref(null);
const {
  rows: keys,
  load,
  loading,
  error,
  total,
  matched,
  table,
  sentinel,
  nextCursor,
  retry,
} = useInfiniteTable(
  (params, signal) => api(`/api-keys?${params}`, { signal }),
  'keys',
  () => emit('signed-out'),
  {
    filters: () => ({ q: query.value }),
    applyResult: (result) => {
      scopes.value = result.scopes;
    },
  },
);

function saved() {
  if (dialog.value.target) {
    dialog.value = null;
    notice.value = 'API key revoked.';
  } else {
    // Keep creation open until the user dismisses the one-time token.
    notice.value = 'API key created.';
  }
  load();
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="API Keys"
      title-id="api-keys-title"
      @open-navigation="emit('open-navigation')"
    >
      <AppButton
        variant="primary"
        :disabled="!scopes.length"
        @click="dialog = {}"
      >
        <Plus :size="18" />
        Create API key
      </AppButton>
    </PageHeading>
    <StatusMessage v-if="notice">{{ notice }}</StatusMessage>
    <section
      class="overflow-hidden rounded-[.65rem] border border-border bg-surface shadow-sm shadow-black/5 dark:shadow-black/20"
      aria-labelledby="api-keys-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">
          {{ total }} {{ total === 1 ? 'API key' : 'API keys' }}
        </span>
        <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
          <AppButton
            variant="icon"
            aria-label="Refresh API keys"
            title="Refresh API keys"
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
              class="sr-only"
              for="search-api-keys"
            >
              Search API keys
            </label>
            <input
              id="search-api-keys"
              v-model="query"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
              type="search"
              placeholder="Search API keys&hellip;"
              autocomplete="off"
            />
          </div>
        </div>
      </div>
      <div
        ref="table"
        class="relative overflow-x-auto"
      >
        <table class="w-full min-w-[36rem] border-collapse text-left whitespace-nowrap">
          <thead>
            <tr>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Name
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Scopes
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Created
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Expires
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody class="[&>tr:last-child>td]:border-b-0">
            <tr v-if="!loading && !error && !keys.length">
              <td
                colspan="5"
                class="p-12 text-center text-sm text-muted"
              >
                {{ query ? 'No matching API keys.' : 'No API keys.' }}
              </td>
            </tr>
            <tr
              v-for="key in keys"
              :key="key.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex items-center gap-3">
                  <KeyRound
                    :size="18"
                    class="shrink-0 text-muted"
                  />
                  <strong
                    class="max-w-72 truncate font-[550]"
                    :title="key.name"
                  >
                    {{ key.name }}
                  </strong>
                </div>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div
                  class="max-w-72 overflow-x-auto whitespace-nowrap text-xs text-muted"
                  tabindex="0"
                  :aria-label="`Scopes for ${key.name}`"
                >
                  {{ key.scopes.join(', ') }}
                </div>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                {{ formatTimestamp(key.created_at) }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                {{ key.expires_at === 0 ? 'No expiration' : formatTimestamp(key.expires_at) }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Revoke ${key.name}`"
                    title="Revoke"
                    @click="dialog = { target: key }"
                  >
                    <Trash2 :size="17" />
                  </AppButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div
        v-if="query"
        class="border-t border-border px-4 py-3 text-xs text-muted"
      >
        {{ matched }} of {{ total }} API keys
      </div>
      <div
        v-if="loading || error || nextCursor"
        ref="sentinel"
      >
        <TableLoadState
          :loading="loading"
          :error="error"
          @retry="retry"
        />
      </div>
    </section>
    <APIKeyDialog
      v-if="dialog"
      :target="dialog.target"
      :scopes="scopes"
      @close="dialog = null"
      @saved="saved"
      @auth-error="emit('auth-error')"
    />
  </main>
</template>
