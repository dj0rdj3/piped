<script setup>
import { computed, onUnmounted, ref, watch } from 'vue';
import { LoaderCircle, Server, Trash2 } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { usePageLoad } from '../../composables/usePageLoad.js';
import { formChanges } from '../../lib/unsaved-changes.js';

const props = defineProps({
  mode: { type: String, required: true },
  target: { type: Object, default: null },
});
const emit = defineEmits(['close', 'saved', 'auth-error']);
const name = ref(props.target?.name || '');
const address = ref(props.target?.address || '');
const port = ref(String(props.target?.port || 22));
const username = ref(props.target?.username || '');
const tags = ref(props.target?.tags?.join(', ') || '');
const authentication = ref(props.target?.credential_id ? 'key' : 'password');
const credentialID = ref(props.target?.credential_id || '');
const password = ref('');
const runAsSudo = ref(props.target?.run_as_sudo || false);
const sudoPassword = ref('');
const clearSudoPassword = ref(false);
const credentials = ref([]);
const error = ref('');
const busy = ref(false);
const formDialog = ref(null);
const dirty = formChanges(
  () => [
    name.value,
    address.value,
    port.value,
    username.value,
    tags.value,
    authentication.value,
    credentialID.value,
    password.value,
    runAsSudo.value,
    sudoPassword.value,
    clearSudoPassword.value,
  ],
  () => props.mode !== 'delete',
);
let mounted = true;
const title = computed(
  () => ({ create: 'Add host', edit: 'Edit host', delete: 'Remove host' })[props.mode],
);
const canKeepPassword = computed(() => props.mode === 'edit' && props.target?.has_password);

watch(authentication, () => {
  password.value = '';
});
watch(runAsSudo, () => {
  sudoPassword.value = '';
  clearSudoPassword.value = false;
});
onUnmounted(() => {
  mounted = false;
  password.value = '';
  sudoPassword.value = '';
});

const {
  load: loadCredentials,
  loading: loadingCredentials,
  error: credentialError,
} = usePageLoad(
  () => api('/credentials'),
  (result) => {
    credentials.value = result.credentials;
  },
  () => emit('auth-error'),
  { loadOnMount: props.mode !== 'delete' },
);

async function submit() {
  if (busy.value) {
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    const body = { version: props.target?.version || 0 };
    const path = props.mode === 'create' ? '/hosts' : `/hosts/${props.target.id}`;
    if (props.mode !== 'delete') {
      Object.assign(body, {
        name: name.value,
        address: address.value,
        port: Number(port.value),
        username: username.value,
        credential_id: authentication.value === 'key' ? credentialID.value : '',
        run_as_sudo: runAsSudo.value,
        tags: tags.value
          .split(',')
          .map((tag) => tag.trim())
          .filter(Boolean),
      });
      if (authentication.value === 'password' && (password.value || !canKeepPassword.value)) {
        body.password = password.value;
      }
      if (runAsSudo.value && clearSudoPassword.value) {
        body.sudo_password = '';
      } else if (runAsSudo.value && sudoPassword.value) {
        body.sudo_password = sudoPassword.value;
      }
    }
    const method = { create: 'POST', edit: 'PUT', delete: 'DELETE' }[props.mode];
    await api(path, { method, body });
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
    password.value = '';
    sudoPassword.value = '';
    busy.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :dirty="dirty"
    :title="title"
    title-id="host-dialog-title"
    :busy="busy"
    :wide="mode !== 'delete'"
    @close="emit('close')"
  >
    <template #icon>
      <Trash2
        v-if="mode === 'delete'"
        :size="21"
      />
      <Server
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
        and its stored passwords?
      </p>
      <fieldset
        v-else
        :disabled="busy"
        class="min-w-0 border-0 p-0"
      >
        <div class="my-4">
          <label
            for="host-name"
            class="mb-2 block text-sm font-[550]"
          >
            Name
          </label>
          <FormInput
            id="host-name"
            v-model="name"
            required
            maxlength="100"
            autocomplete="off"
            autofocus
          />
        </div>
        <div class="my-4 grid grid-cols-[minmax(0,1fr)_6rem] gap-3">
          <div>
            <label
              for="host-address"
              class="mb-2 block text-sm font-[550]"
            >
              IP address or DNS name
            </label>
            <FormInput
              id="host-address"
              v-model="address"
              required
              maxlength="253"
              autocomplete="off"
              placeholder="server.example.com"
              spellcheck="false"
            />
          </div>
          <div>
            <label
              for="host-port"
              class="mb-2 block text-sm font-[550]"
            >
              SSH port
            </label>
            <FormInput
              id="host-port"
              v-model="port"
              type="number"
              min="1"
              max="65535"
              step="1"
              required
            />
          </div>
        </div>
        <div class="my-4">
          <label
            for="host-tags"
            class="mb-2 block text-sm font-[550]"
          >
            Tags
            <span class="font-normal text-muted">(optional)</span>
          </label>
          <FormInput
            id="host-tags"
            v-model="tags"
            maxlength="1040"
            autocomplete="off"
            placeholder="production, linux"
          />
          <p class="mt-1.5 text-xs text-muted">Separate tags with commas. Up to 20 tags.</p>
        </div>
        <div class="my-4 border-t border-border pt-4">
          <label
            for="host-username"
            class="mb-2 block text-sm font-[550]"
          >
            SSH username
          </label>
          <FormInput
            id="host-username"
            v-model="username"
            required
            maxlength="128"
            autocomplete="off"
            spellcheck="false"
          />
        </div>
        <div class="my-4">
          <label
            for="host-authentication"
            class="mb-2 block text-sm font-[550]"
          >
            Authentication
          </label>
          <select
            id="host-authentication"
            v-model="authentication"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option value="password">Host password</option>
            <option value="key">SSH key credential</option>
          </select>
        </div>
        <div
          v-if="authentication === 'key'"
          class="my-4"
        >
          <label
            for="host-credential"
            class="mb-2 block text-sm font-[550]"
          >
            SSH key
          </label>
          <select
            id="host-credential"
            v-model="credentialID"
            required
            :disabled="loadingCredentials || !!credentialError"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option
              value=""
              disabled
            >
              {{ loadingCredentials ? 'Loading keys...' : 'Select an SSH key' }}
            </option>
            <option
              v-for="credential in credentials"
              :key="credential.id"
              :value="credential.id"
            >
              {{ credential.name }}
            </option>
          </select>
          <StatusMessage
            v-if="credentialError"
            variant="error"
          >
            {{ credentialError }}
            <template #action>
              <AppButton
                variant="text"
                @click="loadCredentials"
              >
                Retry
              </AppButton>
            </template>
          </StatusMessage>
          <p
            v-else-if="!loadingCredentials && !credentials.length"
            class="mt-1.5 text-xs text-muted"
          >
            Import or generate a key on the Credentials page first.
          </p>
          <p
            v-if="target?.has_password"
            class="mt-1.5 text-xs text-muted"
          >
            Saving with an SSH key removes the stored SSH password.
          </p>
        </div>
        <div
          v-else
          class="my-4"
        >
          <label
            for="host-password"
            class="mb-2 block text-sm font-[550]"
          >
            SSH password
          </label>
          <FormInput
            id="host-password"
            v-model="password"
            type="password"
            maxlength="1024"
            autocomplete="new-password"
            :required="!canKeepPassword"
            :placeholder="canKeepPassword ? 'Leave empty to keep stored password' : ''"
          />
        </div>
        <div class="my-4 border-t border-border pt-4">
          <label class="inline-flex items-center gap-2.5 text-sm font-[550]">
            <input
              v-model="runAsSudo"
              type="checkbox"
              class="size-4 accent-accent"
            />
            Allow sudo
          </label>
          <p class="mt-1.5 text-xs text-muted">
            Used only when a job enables Use sudo. Root connections run directly.
          </p>
          <p
            v-if="!runAsSudo && target?.has_sudo_password"
            class="mt-1.5 text-xs text-muted"
          >
            Saving with sudo disabled removes the stored sudo password.
          </p>
        </div>
        <div
          v-if="runAsSudo"
          class="my-4"
        >
          <label
            for="host-sudo-password"
            class="mb-2 block text-sm font-[550]"
          >
            Sudo password
            <span class="font-normal text-muted">(optional)</span>
          </label>
          <FormInput
            id="host-sudo-password"
            v-model="sudoPassword"
            type="password"
            maxlength="1024"
            autocomplete="new-password"
            :disabled="clearSudoPassword"
            :placeholder="
              target?.has_sudo_password && !clearSudoPassword
                ? 'Leave empty to keep stored password'
                : 'Leave empty for passwordless sudo'
            "
          />
          <label
            v-if="target?.has_sudo_password"
            class="mt-2.5 inline-flex items-center gap-2.5 text-sm text-muted"
          >
            <input
              v-model="clearSudoPassword"
              type="checkbox"
              class="size-4 accent-accent"
            />
            Remove stored sudo password
          </label>
        </div>
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
          :disabled="
            busy ||
            (mode !== 'delete' &&
              authentication === 'key' &&
              (loadingCredentials || !!credentialError || !credentialID))
          "
        >
          <LoaderCircle
            v-if="busy"
            class="motion-safe:animate-spin"
            :size="17"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else-if="mode === 'delete'">Remove host</template>
          <template v-else>{{ mode === 'create' ? 'Add host' : 'Save changes' }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
