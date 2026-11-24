<script setup>
import { computed, onUnmounted, ref, watch } from 'vue';
import {
  Check,
  Copy,
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  Upload,
} from '@lucide/vue';
import CredentialDialog from './CredentialDialog.vue';
import AppButton from '../../components/ui/AppButton.vue';
import PageHeading from '../../components/ui/PageHeading.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { usePageLoad } from '../../composables/usePageLoad.js';

const emit = defineEmits(['signed-out', 'auth-error', 'open-navigation']);
const props = defineProps({ requestedAction: { type: Object, default: null } });
const credentials = ref([]);
const query = ref('');
const notice = ref('');
const dialog = ref(null);
const copying = ref(false);
const copiedID = ref('');
let copiedTimer;
let mounted = true;

watch(
  () => props.requestedAction,
  (action) => {
    if (action && ['import', 'generate'].includes(action.mode)) {
      dialog.value = { mode: action.mode, target: { name: action.name } };
    }
  },
  { immediate: true },
);

const saveMessages = {
  import: 'SSH key imported.',
  generate: 'SSH key generated.',
  rename: 'Credential renamed.',
  delete: 'Credential removed.',
};
const filtered = computed(() => {
  const search = query.value.toLowerCase();
  return credentials.value.filter((credential) => {
    const searchable = [credential.name, credential.fingerprint, credential.algorithm].join(' ');
    return searchable.toLowerCase().includes(search);
  });
});

onUnmounted(() => {
  mounted = false;
  window.clearTimeout(copiedTimer);
});

async function copyPublicKey(credential) {
  if (copying.value) {
    return;
  }
  copying.value = true;
  error.value = '';
  notice.value = '';
  try {
    await navigator.clipboard.writeText(credential.public_key);
    if (mounted) {
      window.clearTimeout(copiedTimer);
      copiedID.value = credential.id;
      copiedTimer = window.setTimeout(() => {
        copiedID.value = '';
      }, 2000);
    }
  } catch {
    if (mounted) {
      error.value = 'Could not copy the public key. Open Public key to copy it manually.';
    }
  } finally {
    copying.value = false;
  }
}

const { load, loading, error } = usePageLoad(
  () => api('/credentials'),
  (result) => {
    credentials.value = result.credentials;
  },
  () => emit('signed-out'),
);

async function saved(credential) {
  const mode = dialog.value.mode;
  dialog.value = credential ? { mode: 'public', target: credential } : null;
  notice.value = saveMessages[mode];
  await load();
}

function keyLabel(algorithm) {
  const labels = { 'ssh-ed25519': 'Ed25519', 'ssh-rsa': 'RSA' };
  return labels[algorithm] || 'ECDSA';
}
</script>

<template>
  <main class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]">
    <PageHeading
      title="Credentials"
      title-id="credentials-title"
      @open-navigation="emit('open-navigation')"
    >
      <div class="flex flex-wrap gap-2">
        <AppButton
          variant="secondary"
          @click="dialog = { mode: 'import' }"
        >
          <Upload :size="17" />
          Import key
        </AppButton>
        <AppButton
          variant="primary"
          @click="dialog = { mode: 'generate' }"
        >
          <Plus :size="18" />
          Generate key
        </AppButton>
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
      aria-labelledby="credentials-title"
      :aria-busy="loading"
    >
      <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span class="text-sm text-muted">
          {{ credentials.length }} {{ credentials.length === 1 ? 'credential' : 'credentials' }}
        </span>
        <div class="flex min-w-0 items-center gap-2 max-[560px]:w-full">
          <AppButton
            variant="icon"
            aria-label="Refresh credentials"
            title="Refresh credentials"
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
              for="search-credentials"
            >
              Search credentials
            </label>
            <input
              id="search-credentials"
              v-model="query"
              class="w-44 min-w-0 border-0 bg-transparent p-0 text-sm text-foreground outline-none placeholder:text-muted max-[560px]:w-full"
              type="search"
              placeholder="Search credentials&hellip;"
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
                Key
              </th>
              <th
                class="border-y border-border bg-surface-muted px-4 py-2.5 text-sm font-medium text-muted"
                scope="col"
              >
                Fingerprint
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
              v-for="credential in filtered"
              :key="credential.id"
              class="h-16 hover:bg-surface-hover [&>td]:align-middle"
            >
              <td class="border-b border-border text-sm px-4 py-2.5">
                <div
                  class="flex items-center gap-3 [&>strong]:max-w-72 [&>strong]:truncate [&>strong]:font-[550]"
                >
                  <KeyRound
                    :size="18"
                    class="shrink-0 text-muted"
                  />
                  <strong>{{ credential.name }}</strong>
                </div>
              </td>
              <td class="border-b border-border text-sm px-4 py-2.5">
                <span class="inline-flex items-center gap-1.5 text-sm text-muted">
                  {{ keyLabel(credential.algorithm) }}
                </span>
                <span
                  v-if="credential.has_passphrase"
                  class="mt-1 block text-xs text-muted"
                >
                  Passphrase protected
                </span>
              </td>
              <td class="border-b border-border text-sm px-4 py-2.5">
                <code class="text-xs text-muted">{{ credential.fingerprint }}</code>
              </td>
              <td class="border-b border-border text-sm px-4 py-2.5">
                <div class="flex justify-end gap-1.5">
                  <AppButton
                    variant="icon"
                    :aria-disabled="copying"
                    :aria-label="`Copy public key for ${credential.name}`"
                    :title="copiedID === credential.id ? 'Copied' : 'Copy public key'"
                    @click="copyPublicKey(credential)"
                  >
                    <Check
                      v-if="copiedID === credential.id"
                      :size="17"
                      class="text-accent-text"
                    />
                    <Copy
                      v-else
                      :size="17"
                    />
                    <span
                      v-if="copiedID === credential.id"
                      class="sr-only"
                      role="status"
                    >
                      Public key copied.
                    </span>
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Show public key for ${credential.name}`"
                    title="Public key"
                    @click="dialog = { mode: 'public', target: credential }"
                  >
                    <KeyRound :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    :aria-label="`Rename ${credential.name}`"
                    title="Rename"
                    @click="dialog = { mode: 'rename', target: credential }"
                  >
                    <Pencil :size="17" />
                  </AppButton>
                  <AppButton
                    variant="icon"
                    danger
                    :aria-label="`Remove ${credential.name}`"
                    title="Remove"
                    @click="dialog = { mode: 'delete', target: credential }"
                  >
                    <Trash2 :size="17" />
                  </AppButton>
                </div>
              </td>
            </tr>
            <tr v-if="loading && !credentials.length">
              <td
                colspan="4"
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
                colspan="4"
                class="p-12 text-center text-sm text-muted"
              >
                {{
                  query
                    ? 'No matching credentials.'
                    : 'No SSH keys. Import or generate a key to get started.'
                }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div
        v-if="query"
        class="border-t border-border px-4 py-3 text-xs text-muted"
      >
        {{ filtered.length }} of {{ credentials.length }} credentials
      </div>
    </section>
    <CredentialDialog
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
