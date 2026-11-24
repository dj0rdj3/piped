<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { KeyRound, LoaderCircle, Trash2, UserPlus } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { formChanges } from '../../lib/unsaved-changes.js';

const props = defineProps({
  mode: { type: String, required: true },
  target: { type: Object, default: null },
  currentUser: { type: Object, required: true },
});
const emit = defineEmits(['close', 'saved', 'signed-out', 'auth-error']);
const username = ref('');
const password = ref('');
const confirmation = ref('');
const currentPassword = ref('');
const error = ref('');
const busy = ref(false);
const formDialog = ref(null);
const dirty = formChanges(() => [
  username.value,
  password.value,
  confirmation.value,
  currentPassword.value,
]);
let mounted = true;

const modes = {
  create: { title: 'Add user', action: 'Create account', notice: 'Account created.' },
  password: { title: 'Change password', action: 'Update password', notice: 'Password updated.' },
  delete: { title: 'Remove user', action: 'Remove account', notice: 'Account removed.' },
};
const currentMode = computed(() => modes[props.mode]);

onUnmounted(() => {
  mounted = false;
  password.value = '';
  confirmation.value = '';
  currentPassword.value = '';
});

async function submit() {
  if (busy.value) {
    return;
  }

  error.value = '';
  if (props.mode !== 'delete' && password.value !== confirmation.value) {
    error.value = 'Passwords do not match.';
    return;
  }

  busy.value = true;
  try {
    const body = { current_password: currentPassword.value };
    if (props.mode === 'create') {
      await api('/users', {
        method: 'POST',
        body: { ...body, username: username.value, password: password.value },
      });
    } else if (props.mode === 'password') {
      await api(`/users/${props.target.id}/password`, {
        method: 'POST',
        body: { ...body, password: password.value },
      });
      if (!mounted) {
        return;
      }
      if (props.target.id === props.currentUser.id) {
        emit('signed-out');
        return;
      }
    } else {
      await api(`/users/${props.target.id}`, { method: 'DELETE', body });
    }

    if (mounted) {
      emit('saved', currentMode.value.notice);
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
    currentPassword.value = '';
    busy.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :dirty="dirty"
    :title="currentMode.title"
    title-id="user-dialog-title"
    :busy="busy"
    @close="emit('close')"
  >
    <template #icon>
      <UserPlus
        v-if="mode === 'create'"
        :size="21"
      />
      <KeyRound
        v-else-if="mode === 'password'"
        :size="21"
      />
      <Trash2
        v-else
        :size="21"
      />
    </template>

    <p
      v-if="mode === 'password'"
      class="text-sm leading-relaxed text-muted"
    >
      <strong>{{ target.username }}</strong>
      - Signs out all sessions.
    </p>
    <p
      v-if="mode === 'delete'"
      class="text-sm leading-relaxed text-muted"
    >
      Permanently remove
      <strong>{{ target.username }}</strong>
      ?
    </p>

    <form @submit.prevent="submit">
      <div
        v-if="mode === 'create'"
        class="my-4"
      >
        <label
          for="new-username"
          class="mb-2 block text-sm font-[550]"
        >
          Username
        </label>
        <FormInput
          id="new-username"
          v-model="username"
          required
          minlength="3"
          maxlength="64"
          pattern="[a-z0-9][a-z0-9._\-]{2,63}"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          autofocus
          :disabled="busy"
        />
        <p class="mt-1.5 text-xs text-muted">3-64 characters: a-z, 0-9, . _ -</p>
      </div>

      <template v-if="mode !== 'delete'">
        <div class="my-4">
          <label
            for="new-password"
            class="mb-2 block text-sm font-[550]"
          >
            {{ mode === 'create' ? 'Password' : 'New password' }}
          </label>
          <FormInput
            id="new-password"
            v-model="password"
            type="password"
            autocomplete="new-password"
            required
            minlength="8"
            maxlength="128"
            :disabled="busy"
          />
          <p class="mt-1.5 text-xs text-muted">8-128 characters</p>
        </div>
        <div class="my-4">
          <label
            for="confirm-password"
            class="mb-2 block text-sm font-[550]"
          >
            Confirm {{ mode === 'password' ? 'new ' : '' }}password
          </label>
          <FormInput
            id="confirm-password"
            v-model="confirmation"
            type="password"
            autocomplete="new-password"
            required
            :disabled="busy"
          />
        </div>
      </template>

      <div class="my-4 border-t border-border pt-4">
        <label
          for="current-password"
          class="mb-2 block text-sm font-[550]"
        >
          Your current password
        </label>
        <FormInput
          id="current-password"
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
          :variant="mode === 'delete' ? 'danger' : 'primary'"
          :disabled="busy"
        >
          <LoaderCircle
            v-if="busy"
            class="motion-safe:animate-spin"
            :size="17"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else>{{ currentMode.action }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
