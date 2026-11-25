<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { Check, Copy, KeyRound, LoaderCircle, Trash2 } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { formChanges } from '../../lib/unsaved-changes.js';

const props = defineProps({
  target: { type: Object, default: null },
  scopes: { type: Array, required: true },
});
const emit = defineEmits(['close', 'saved', 'auth-error']);
const name = ref('');
const selectedScopes = ref([]);
const expiresInHours = ref('720');
const currentPassword = ref('');
const token = ref('');
const error = ref('');
const busy = ref(false);
const copying = ref(false);
const copied = ref(false);
const formDialog = ref(null);
const formDirty = formChanges(() => [
  name.value,
  selectedScopes.value,
  expiresInHours.value,
  currentPassword.value,
]);
const dirty = computed(() => !!token.value || formDirty.value);
const title = computed(() => {
  if (token.value) {
    return 'API key created';
  }
  return props.target ? 'Revoke API key' : 'Create API key';
});
let mounted = true;
let copyTimer;

onUnmounted(() => {
  mounted = false;
  token.value = '';
  currentPassword.value = '';
  window.clearTimeout(copyTimer);
});

async function submit() {
  if (busy.value || token.value) {
    return;
  }
  error.value = '';
  if (!props.target && selectedScopes.value.length === 0) {
    error.value = 'Select at least one scope.';
    return;
  }
  busy.value = true;
  try {
    const body = { current_password: currentPassword.value };
    if (!props.target) {
      Object.assign(body, {
        name: name.value,
        scopes: [...selectedScopes.value],
        expires_in_hours: Number(expiresInHours.value),
      });
    }
    const path = props.target ? `/api-keys/${props.target.id}` : '/api-keys';
    const method = props.target ? 'DELETE' : 'POST';
    const result = await api(path, { method, body });
    if (mounted) {
      if (!props.target) {
        token.value = result.token;
      }
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
    currentPassword.value = '';
    busy.value = false;
  }
}

async function copyToken() {
  if (copying.value || !token.value) {
    return;
  }
  copying.value = true;
  error.value = '';
  try {
    await navigator.clipboard.writeText(token.value);
    if (mounted) {
      copied.value = true;
      window.clearTimeout(copyTimer);
      copyTimer = window.setTimeout(() => {
        copied.value = false;
      }, 2000);
    }
  } catch {
    if (mounted) {
      error.value = 'Could not copy the key. Select and copy it manually.';
    }
  } finally {
    copying.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :title="title"
    title-id="api-key-dialog-title"
    :busy="busy"
    :dirty="dirty"
    @close="emit('close')"
  >
    <template #icon>
      <Trash2
        v-if="target"
        :size="21"
      />
      <KeyRound
        v-else
        :size="21"
      />
    </template>
    <template v-if="token">
      <p class="text-sm leading-relaxed text-muted">
        Copy this key now. It will not be shown again.
      </p>
      <div class="my-4">
        <FormInput
          :model-value="token"
          aria-label="New API key"
          readonly
          multiline
          rows="3"
          spellcheck="false"
          autocomplete="off"
          @focus="$event.target.select()"
        />
      </div>
      <StatusMessage
        v-if="error"
        variant="error"
      >
        {{ error }}
      </StatusMessage>
      <div class="mt-5 flex justify-end gap-2.5">
        <AppButton @click="emit('close')">Close</AppButton>
        <AppButton
          variant="primary"
          :aria-disabled="copying"
          aria-label="Copy API key"
          :title="copied ? 'Copied' : 'Copy API key'"
          @click="copyToken"
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
            API key copied.
          </span>
        </AppButton>
      </div>
    </template>
    <form
      v-else
      @submit.prevent="submit"
    >
      <p
        v-if="target"
        class="text-sm leading-relaxed text-muted"
      >
        Revoke
        <strong class="[overflow-wrap:anywhere]">{{ target.name }}</strong>
        ? Applications using this key will lose access immediately.
      </p>
      <template v-else>
        <div class="my-4">
          <label
            for="api-key-name"
            class="mb-2 block text-sm font-[550]"
          >
            Name
          </label>
          <FormInput
            id="api-key-name"
            v-model="name"
            required
            maxlength="100"
            autocomplete="off"
            autofocus
            :disabled="busy"
          />
        </div>
        <div class="my-4">
          <label
            for="api-key-expiry"
            class="mb-2 block text-sm font-[550]"
          >
            Expires in
          </label>
          <select
            id="api-key-expiry"
            v-model="expiresInHours"
            :disabled="busy"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option value="1">1h</option>
            <option value="24">1d</option>
            <option value="168">7d</option>
            <option value="720">30d</option>
            <option value="2160">90d</option>
            <option value="0">No expiration</option>
          </select>
        </div>
        <fieldset
          class="my-4 min-w-0 border-0 p-0"
          :disabled="busy"
        >
          <legend class="mb-2 text-sm font-[550]">Scopes</legend>
          <div class="grid grid-cols-2 gap-2 max-[400px]:grid-cols-1">
            <label
              v-for="scope in scopes"
              :key="scope"
              class="inline-flex cursor-pointer items-center gap-2 text-sm"
            >
              <input
                v-model="selectedScopes"
                type="checkbox"
                :value="scope"
                class="size-4 shrink-0 accent-accent"
              />
              {{ scope }}
            </label>
          </div>
          <p class="mt-2 text-xs text-muted">
            Write scopes allow changes. runs:write allows command execution.
          </p>
        </fieldset>
      </template>
      <div class="my-4 border-t border-border pt-4">
        <label
          for="api-key-password"
          class="mb-2 block text-sm font-[550]"
        >
          Your current password
        </label>
        <FormInput
          id="api-key-password"
          v-model="currentPassword"
          type="password"
          autocomplete="current-password"
          required
          :disabled="busy"
        />
      </div>
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
          :variant="target ? 'danger' : 'primary'"
          :disabled="busy"
        >
          <LoaderCircle
            v-if="busy"
            class="motion-safe:animate-spin"
            :size="17"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else>{{ target ? 'Revoke key' : 'Create key' }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
