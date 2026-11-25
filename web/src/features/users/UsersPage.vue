<script setup>
import { ref } from 'vue';
import { KeyRound, Plus, RefreshCw, Search, Trash2 } from '@lucide/vue';
import UserDialog from './UserDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { useInfiniteTable } from '../../composables/useInfiniteTable.js';
import TableLoadState from '../../components/ui/TableLoadState.vue';
import { formatTimestamp } from '../../lib/date-time.js';

const props = defineProps({ currentUser: { type: Object, required: true } });
const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const query = ref('');
const notice = ref('');
const dialog = ref(null);

const {
  rows: users,
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
  (params, signal) => api(`/users?${params}`, { signal }),
  'users',
  () => emit('signed-out', 'Session expired. Sign in again.'),
  { filters: () => ({ q: query.value }) },
);

async function saved(message) {
  dialog.value = null;
  notice.value = message;
  await load();
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="Users"
      title-id="members-title"
      @open-navigation="emit('open-navigation')"
    >
      <AppButton
        variant="primary"
        @click="dialog = { mode: 'create' }"
      >
        <Plus :size="18" />
        Add user
      </AppButton>
    </PageHeading>
    <StatusMessage v-if="notice">{{ notice }}</StatusMessage>
    <section
      class="overflow-hidden rounded-[.65rem] border border-border bg-surface shadow-sm shadow-black/5 dark:shadow-black/20"
      aria-labelledby="members-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">{{ total }} {{ total === 1 ? 'user' : 'users' }}</span>
        <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
          <AppButton
            variant="icon"
            aria-label="Refresh users"
            title="Refresh users"
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
              for="search-users"
            >
              Search users
            </label>
            <input
              id="search-users"
              v-model="query"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
              type="search"
              placeholder="Search users&hellip;"
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
                User
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Added
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
            <tr
              v-for="member in users"
              :key="member.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="border-b border-border text-sm px-4 py-2.5">
                <div
                  class="flex items-center gap-3 [&>strong]:max-w-72 [&>strong]:truncate [&>strong]:font-[550]"
                >
                  <span
                    class="grid size-[2.1rem] shrink-0 place-items-center rounded-full border border-border bg-surface-muted text-xs font-[550] text-muted"
                  >
                    {{ member.username.slice(0, 2).toUpperCase() }}
                  </span>
                  <strong>{{ member.username }}</strong>
                  <span
                    v-if="member.id === currentUser.id"
                    class="rounded border border-border bg-surface-muted px-1.5 py-0.5 text-xs text-muted"
                  >
                    You
                  </span>
                </div>
              </td>
              <td class="text-muted border-b border-border text-sm px-4 py-2.5">
                {{ formatTimestamp(member.created_at) }}
              </td>
              <td class="border-b border-border text-sm px-4 py-2.5">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    :aria-label="`Change password for ${member.username}`"
                    :title="`Change password for ${member.username}`"
                    @click="dialog = { mode: 'password', target: member }"
                  >
                    <KeyRound :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Remove ${member.username}`"
                    :title="
                      member.id === currentUser.id
                        ? 'You cannot remove your own account'
                        : `Remove ${member.username}`
                    "
                    :disabled="member.id === currentUser.id"
                    @click="dialog = { mode: 'delete', target: member }"
                  >
                    <Trash2 :size="17" />
                  </AppButton>
                </div>
              </td>
            </tr>
            <tr v-if="!loading && !error && !users.length">
              <td
                colspan="3"
                class="p-12 text-center text-sm text-muted"
              >
                {{ query ? 'No matching users.' : 'No users.' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div
        v-if="query"
        class="border-t border-border px-4 py-3 text-xs text-muted"
      >
        {{ matched }} of {{ total }} users
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
    <UserDialog
      v-if="dialog"
      :mode="dialog.mode"
      :target="dialog.target"
      :current-user="props.currentUser"
      @close="dialog = null"
      @saved="saved"
      @signed-out="emit('signed-out', 'Password updated. Sign in again.')"
      @auth-error="emit('auth-error')"
    />
  </main>
</template>
