<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue';
import {
  Bell,
  ChevronDown,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  Webhook,
} from '@lucide/vue';
import WebhookDialog from './WebhookDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { usePageLoad } from '../../composables/usePageLoad.js';

const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const targets = ref([]);
const query = ref('');
const filteredTargets = computed(() => {
  const search = query.value.toLowerCase();
  return targets.value.filter((target) => target.name.toLowerCase().includes(search));
});
const notice = ref('');
const dialog = ref(null);
const addMenuOpen = ref(false);
const addControl = ref(null);

async function showAddMenu(last = false) {
  addMenuOpen.value = true;
  await nextTick();
  const items = addControl.value?.querySelectorAll('[role="menuitem"]');
  const item = last ? items?.[items.length - 1] : items?.[0];
  item?.focus();
}

function closeAddMenu(restoreFocus = false) {
  addMenuOpen.value = false;
  if (restoreFocus) {
    addControl.value?.querySelector('[aria-haspopup="menu"]')?.focus();
  }
}

function dismissAddMenu(event) {
  if (!addControl.value?.contains(event.target)) {
    closeAddMenu();
  }
}

function moveMenuFocus(event) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    return;
  }
  event.preventDefault();
  const items = [...addControl.value.querySelectorAll('[role="menuitem"]')];
  const current = items.indexOf(event.target);
  let next = (current + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length;
  if (event.key === 'Home') {
    next = 0;
  } else if (event.key === 'End') {
    next = items.length - 1;
  }
  items[next]?.focus();
}

function createTarget(type) {
  closeAddMenu();
  dialog.value = { mode: 'create', type };
}

onMounted(() => window.addEventListener('pointerdown', dismissAddMenu));
onUnmounted(() => window.removeEventListener('pointerdown', dismissAddMenu));
const { load, loading, error } = usePageLoad(
  () => api('/notifications'),
  (result) => {
    targets.value = result.targets;
  },
  () => emit('signed-out'),
);

function saved() {
  dialog.value = null;
  notice.value = 'Notification targets updated.';
  load();
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="Notifications"
      title-id="notifications-title"
      @open-navigation="emit('open-navigation')"
    >
      <div
        ref="addControl"
        class="relative"
        @keydown.esc.stop.prevent="closeAddMenu(true)"
        @focusout="!$event.currentTarget.contains($event.relatedTarget) && closeAddMenu()"
      >
        <AppButton
          variant="primary"
          aria-haspopup="menu"
          :aria-expanded="addMenuOpen"
          aria-controls="notification-add-menu"
          @click="addMenuOpen ? closeAddMenu() : showAddMenu()"
          @keydown.down.prevent="showAddMenu()"
          @keydown.up.prevent="showAddMenu(true)"
        >
          <Plus :size="18" />
          Add target
          <ChevronDown :size="16" />
        </AppButton>
        <div
          v-if="addMenuOpen"
          id="notification-add-menu"
          role="menu"
          aria-label="Notification type"
          class="absolute right-0 z-20 mt-2 min-w-44 rounded-lg border border-border bg-surface p-1 shadow-lg shadow-black/10 dark:shadow-black/30"
          @keydown="moveMenuFocus"
        >
          <button
            type="button"
            role="menuitem"
            class="flex w-full cursor-pointer items-center gap-2.5 rounded-md border-0 bg-transparent px-3 py-2.5 text-left text-sm text-foreground hover:bg-surface-hover focus:bg-surface-hover focus:outline-none"
            @click="createTarget('webhook')"
          >
            <Webhook
              :size="17"
              class="text-muted"
            />
            Webhook
          </button>
        </div>
      </div>
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
      aria-labelledby="notifications-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">
          {{ targets.length }} {{ targets.length === 1 ? 'target' : 'targets' }}
        </span>
        <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
          <AppButton
            variant="icon"
            aria-label="Refresh targets"
            title="Refresh targets"
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
              for="search-notifications"
            >
              Search targets
            </label>
            <input
              id="search-notifications"
              v-model="query"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
              type="search"
              placeholder="Search targets&hellip;"
              autocomplete="off"
            />
          </div>
        </div>
      </div>
      <div class="relative overflow-x-auto">
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
                Type
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Enabled
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Default for new jobs
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
            <tr v-if="loading && !targets.length">
              <td
                colspan="5"
                class="p-12 text-center text-sm text-muted"
              >
                <LoaderCircle
                  class="mr-2 inline align-middle motion-safe:animate-spin"
                  :size="20"
                />
                Loading&hellip;
              </td>
            </tr>
            <tr v-else-if="!filteredTargets.length">
              <td
                colspan="5"
                class="p-12 text-center text-sm text-muted"
              >
                {{ query ? 'No matching targets.' : 'No targets.' }}
              </td>
            </tr>
            <tr
              v-for="target in filteredTargets"
              :key="target.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex items-center gap-3">
                  <Bell
                    :size="18"
                    class="shrink-0 text-muted"
                  />
                  <strong
                    class="max-w-72 truncate font-[550]"
                    :title="target.name"
                  >
                    {{ target.name }}
                  </strong>
                </div>
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                {{ target.type === 'webhook' ? 'Webhook' : target.type }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                {{ target.enabled ? 'Yes' : 'No' }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm text-muted">
                {{ target.default_enabled ? 'On' : 'Off' }}
              </td>
              <td class="border-b border-border px-4 py-2.5 text-sm">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    :aria-label="`Edit ${target.name}`"
                    title="Edit"
                    @click="dialog = { mode: 'edit', type: target.type, target }"
                  >
                    <Pencil :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Remove ${target.name}`"
                    title="Remove"
                    @click="dialog = { mode: 'delete', type: target.type, target }"
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
        {{ filteredTargets.length }} of {{ targets.length }} targets
      </div>
    </section>
    <WebhookDialog
      v-if="dialog?.type === 'webhook'"
      :target="dialog.target"
      :mode="dialog.mode"
      @close="dialog = null"
      @saved="saved"
      @auth-error="emit('auth-error')"
    />
  </main>
</template>
