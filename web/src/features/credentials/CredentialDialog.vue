<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { Check, Copy, KeyRound, LoaderCircle, Trash2 } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { formChanges } from '../../lib/unsaved-changes.js';
import { readKeyFile } from './key-files.js';

const props = defineProps({
  mode: { type: String, required: true },
  target: { type: Object, default: null },
});
const emit = defineEmits(['close', 'saved', 'auth-error']);
const name = ref(props.target?.name || '');
const privateKey = ref('');
const publicKey = ref('');
const passphrase = ref('');
const error = ref('');
const busy = ref(false);
const copied = ref(false);
const copying = ref(false);
let copiedTimer;
const readingFiles = ref(0);
const fileRequests = { public: 0, private: 0 };
const formDialog = ref(null);
const dirty = formChanges(
  () => [name.value, privateKey.value, publicKey.value, passphrase.value],
  () => !['delete', 'public'].includes(props.mode),
);
let mounted = true;

const modes = {
  import: { title: 'Import SSH key', action: 'Import key' },
  generate: { title: 'Generate SSH key', action: 'Generate key' },
  rename: { title: 'Rename credential', action: 'Save name' },
  delete: { title: 'Remove credential', action: 'Remove credential' },
  public: { title: 'Public key' },
};
const currentMode = computed(() => modes[props.mode]);

onUnmounted(() => {
  mounted = false;
  window.clearTimeout(copiedTimer);
  privateKey.value = '';
  publicKey.value = '';
  passphrase.value = '';
});

async function loadKeyFile(event, kind) {
  if (!event.target.files?.length) {
    return;
  }

  const field = kind === 'public' ? publicKey : privateKey;
  const previousValue = field.value;
  const requestID = ++fileRequests[kind];
  const limit = kind === 'public' ? 8192 : 32768;
  error.value = '';
  readingFiles.value++;

  try {
    const text = await readKeyFile(event.target, limit);
    const currentRequest = mounted && requestID === fileRequests[kind];
    if (currentRequest && field.value === previousValue && text !== null) {
      field.value = text;
    }
  } catch (err) {
    if (mounted && requestID === fileRequests[kind]) {
      error.value = err.message;
    }
  } finally {
    readingFiles.value--;
  }
}

async function copyPublicKey() {
  if (copying.value) {
    return;
  }
  copying.value = true;
  error.value = '';
  try {
    await navigator.clipboard.writeText(props.target.public_key);
    if (mounted) {
      window.clearTimeout(copiedTimer);
      copied.value = true;
      copiedTimer = window.setTimeout(() => {
        copied.value = false;
      }, 2000);
    }
  } catch {
    if (mounted) {
      error.value = 'Select and copy the public key below.';
    }
  } finally {
    copying.value = false;
  }
}

async function submit() {
  if (busy.value || readingFiles.value) {
    return;
  }

  busy.value = true;
  error.value = '';
  try {
    let credential = null;

    if (props.mode === 'import' || props.mode === 'generate') {
      const body = { name: name.value, mode: props.mode };
      if (props.mode === 'import') {
        body.private_key = privateKey.value;
        body.public_key = publicKey.value;
        body.passphrase = passphrase.value;
      }
      const result = await api('/credentials', { method: 'POST', body });
      credential = result.credential;
    } else if (props.mode === 'rename') {
      await api(`/credentials/${props.target.id}`, {
        method: 'PATCH',
        body: { name: name.value },
      });
    } else {
      await api(`/credentials/${props.target.id}`, { method: 'DELETE', body: {} });
    }

    privateKey.value = '';
    passphrase.value = '';
    if (mounted) {
      emit('saved', credential);
    }
  } catch (err) {
    if (!mounted) {
      return;
    }
    error.value = err.message;
    if (err.status === 401) {
      emit('auth-error');
    }
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :dirty="dirty"
    :title="currentMode.title"
    title-id="credential-dialog-title"
    :busy="busy"
    :wide="mode !== 'delete'"
    @close="emit('close')"
  >
    <template #icon>
      <Trash2
        v-if="mode === 'delete'"
        :size="21"
      />
      <KeyRound
        v-else
        :size="21"
      />
    </template>

    <template v-if="mode === 'public'">
      <p class="text-sm leading-relaxed text-muted [overflow-wrap:anywhere]">{{ target.name }}</p>
      <div class="my-4">
        <FormInput
          id="credential-public-key"
          aria-label="Public key"
          :model-value="target.public_key"
          multiline
          readonly
          rows="5"
          spellcheck="false"
          @focus="$event.target.select()"
        />
      </div>
      <p class="font-mono text-xs text-muted [overflow-wrap:anywhere]">
        {{ target.fingerprint }}
      </p>
      <StatusMessage
        v-if="error"
        variant="error"
      >
        {{ error }}
      </StatusMessage>
      <div class="mt-5 flex justify-end gap-2.5">
        <AppButton @click="formDialog.requestClose()">Close</AppButton>
        <AppButton
          variant="primary"
          :aria-disabled="copying"
          aria-label="Copy public key"
          :title="copied ? 'Copied' : 'Copy public key'"
          @click="copyPublicKey"
        >
          <Check
            v-if="copied"
            :size="17"
          />
          <Copy
            v-else
            :size="17"
          />
          Copy
          <span
            v-if="copied"
            class="sr-only"
            role="status"
          >
            Public key copied.
          </span>
        </AppButton>
      </div>
    </template>

    <form
      v-else
      @submit.prevent="submit"
    >
      <p
        v-if="mode === 'generate'"
        class="mt-1.5 text-xs text-muted"
      >
        Ed25519 - Private key cannot be viewed or downloaded.
      </p>
      <p
        v-if="mode === 'import'"
        class="mt-1.5 text-xs text-muted"
      >
        RSA, ECDSA, or Ed25519 - Private key cannot be viewed or downloaded after import.
      </p>
      <p
        v-if="mode === 'delete'"
        class="text-sm leading-relaxed text-muted"
      >
        Permanently remove
        <strong class="[overflow-wrap:anywhere]">{{ target.name }}</strong>
        ?
      </p>

      <div
        v-if="mode !== 'delete'"
        class="my-4"
      >
        <label
          for="credential-name"
          class="mb-2 block text-sm font-[550]"
        >
          Name
        </label>
        <FormInput
          id="credential-name"
          v-model="name"
          required
          maxlength="100"
          autocomplete="off"
          autofocus
          :disabled="busy"
        />
      </div>

      <template v-if="mode === 'import'">
        <div class="my-4">
          <label
            for="credential-private-key"
            class="mb-2 block text-sm font-[550]"
          >
            Private key
          </label>
          <FormInput
            id="credential-private-key"
            v-model="privateKey"
            multiline
            required
            rows="5"
            maxlength="32768"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            :disabled="busy"
          />
        </div>
        <div class="my-4">
          <label
            for="credential-private-file"
            class="mb-2 block text-sm font-[550]"
          >
            Private key file
          </label>
          <input
            id="credential-private-file"
            type="file"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
            :disabled="busy"
            @cancel.stop
            @change="loadKeyFile($event, 'private')"
          />
        </div>
        <div class="my-4">
          <label
            for="credential-import-public-key"
            class="mb-2 block text-sm font-[550]"
          >
            Public key
            <span class="font-normal text-muted">(optional)</span>
          </label>
          <FormInput
            id="credential-import-public-key"
            v-model="publicKey"
            multiline
            rows="3"
            maxlength="8192"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            :disabled="busy"
          />
          <p class="mt-1.5 text-xs text-muted">Leave empty to derive from the private key.</p>
        </div>
        <div class="my-4">
          <label
            for="credential-public-file"
            class="mb-2 block text-sm font-[550]"
          >
            Public key file
          </label>
          <input
            id="credential-public-file"
            type="file"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
            :disabled="busy"
            @cancel.stop
            @change="loadKeyFile($event, 'public')"
          />
        </div>
        <div class="my-4">
          <label
            for="credential-passphrase"
            class="mb-2 block text-sm font-[550]"
          >
            Key passphrase
            <span class="font-normal text-muted">(if encrypted)</span>
          </label>
          <FormInput
            id="credential-passphrase"
            v-model="passphrase"
            type="password"
            maxlength="1024"
            autocomplete="off"
            :disabled="busy"
          />
        </div>
      </template>

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
          :disabled="busy || readingFiles > 0"
        >
          <LoaderCircle
            v-if="busy || readingFiles"
            class="motion-safe:animate-spin"
            :size="17"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else-if="readingFiles">Reading key&hellip;</template>
          <template v-else>{{ currentMode.action }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
