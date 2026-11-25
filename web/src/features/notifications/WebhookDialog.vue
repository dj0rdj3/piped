<script setup>
import { computed, onUnmounted, reactive, ref } from 'vue';
import { Bell, LoaderCircle, Trash2 } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import AppDialog from '../../components/ui/AppDialog.vue';
import NamedSecretsEditor from '../../components/ui/NamedSecretsEditor.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';
import { formChanges } from '../../lib/unsaved-changes.js';

const props = defineProps({
  mode: { type: String, required: true },
  target: { type: Object, default: null },
});
const emit = defineEmits(['close', 'saved', 'auth-error']);
const form = reactive({
  name: props.target?.name || '',
  method: props.target?.method || 'POST',
  contentType: props.target?.content_type || 'application/json',
  body: props.target?.body ?? '{"text":"$job on $host: $status\\n$output:10"}',
  enabled: props.target?.enabled ?? true,
  defaultEnabled: props.target?.default_enabled ?? false,
  url: props.target?.endpoint?.url || '',
  headers: JSON.stringify(props.target?.endpoint?.headers || {}, null, 2),
  secrets: (props.target?.secret_names || []).map((name, key) => ({
    key,
    name,
    value: '',
    stored: true,
  })),
});
const error = ref('');
const busy = ref(false);
const formDialog = ref(null);
const title = computed(
  () =>
    ({
      create: 'Add webhook',
      edit: 'Edit webhook',
      delete: 'Remove webhook',
    })[props.mode],
);
const dirty = formChanges(
  () => ({
    ...form,
    secrets: form.secrets.map(({ name, value, stored }) => ({ name, value, stored })),
  }),
  () => props.mode !== 'delete',
);
let mounted = true;
onUnmounted(() => {
  mounted = false;
  form.url = '';
  form.headers = '';
  for (const secret of form.secrets) {
    secret.value = '';
  }
});

async function submit() {
  if (busy.value) {
    return;
  }
  error.value = '';
  let endpoint;
  if (props.mode !== 'delete') {
    let values;
    try {
      values = form.headers.trim() ? JSON.parse(form.headers) : {};
      if (
        !values ||
        Array.isArray(values) ||
        typeof values !== 'object' ||
        Object.values(values).some((value) => typeof value !== 'string')
      ) {
        throw new Error();
      }
    } catch {
      error.value = 'Headers must be a JSON object with string values.';
      return;
    }
    endpoint = { url: form.url, headers: values };
  }
  busy.value = true;
  try {
    const payload = { version: props.target?.version || 0 };
    if (props.mode !== 'delete') {
      Object.assign(payload, {
        type: 'webhook',
        name: form.name,
        method: form.method,
        content_type: form.contentType,
        body: form.body,
        enabled: form.enabled,
        default_enabled: form.defaultEnabled,
        endpoint,
        secrets: form.secrets.map((secret) => ({
          name: secret.name,
          ...(secret.value || !secret.stored ? { value: secret.value } : {}),
        })),
      });
    }
    await api(props.mode === 'create' ? '/notifications' : `/notifications/${props.target.id}`, {
      method: { create: 'POST', edit: 'PUT', delete: 'DELETE' }[props.mode],
      body: payload,
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
    for (const secret of form.secrets) {
      secret.value = '';
    }
    busy.value = false;
  }
}
</script>

<template>
  <AppDialog
    ref="formDialog"
    :dirty="dirty"
    :title="title"
    title-id="notification-dialog-title"
    :busy="busy"
    @close="emit('close')"
  >
    <template #icon>
      <Trash2
        v-if="mode === 'delete'"
        :size="21"
      />
      <Bell
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
        ?
      </p>
      <fieldset
        v-else
        :disabled="busy"
        class="min-w-0 border-0 p-0"
      >
        <div class="my-4">
          <label
            for="notification-name"
            class="mb-2 block text-sm font-[550]"
          >
            Name
          </label>
          <FormInput
            id="notification-name"
            v-model="form.name"
            required
            maxlength="100"
            autocomplete="off"
            autofocus
          />
        </div>
        <div class="my-4">
          <label
            for="notification-url"
            class="mb-2 block text-sm font-[550]"
          >
            URL
          </label>
          <FormInput
            id="notification-url"
            v-model="form.url"
            required
            maxlength="8192"
            autocomplete="off"
            spellcheck="false"
            placeholder="https://example.com/webhook/$token"
          />
        </div>
        <div class="my-4">
          <label
            for="notification-headers"
            class="mb-2 block text-sm font-[550]"
          >
            Headers (JSON)
          </label>
          <textarea
            id="notification-headers"
            v-model="form.headers"
            rows="3"
            maxlength="32768"
            spellcheck="false"
            autocomplete="off"
            placeholder='{"Authorization":"Bearer $token"}'
            class="block w-full resize-y rounded-lg border border-border-strong bg-surface-muted p-3 font-mono text-sm text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          ></textarea>
        </div>
        <NamedSecretsEditor
          v-model="form.secrets"
          id-prefix="notification-secret"
          description="Use $name in the body, URL path or query, or header values. Names are case-insensitive. Values cannot be read after saving."
        />
        <div class="my-4">
          <label
            for="notification-method"
            class="mb-2 block text-sm font-[550]"
          >
            Method
          </label>
          <select
            id="notification-method"
            v-model="form.method"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option>POST</option>
            <option>PUT</option>
            <option>GET</option>
          </select>
        </div>
        <div class="my-4">
          <label
            for="notification-format"
            class="mb-2 block text-sm font-[550]"
          >
            Body format
          </label>
          <select
            id="notification-format"
            v-model="form.contentType"
            class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          >
            <option value="application/json">JSON</option>
            <option value="text/plain">Plain text</option>
          </select>
        </div>
        <div class="my-4">
          <label
            for="notification-body"
            class="mb-2 block text-sm font-[550]"
          >
            Body template
          </label>
          <textarea
            id="notification-body"
            v-model="form.body"
            rows="5"
            maxlength="65536"
            spellcheck="false"
            autocomplete="off"
            aria-describedby="notification-template-help"
            class="block w-full resize-y rounded-lg border border-border-strong bg-surface-muted p-3 font-mono text-sm text-foreground focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring"
          ></textarea>
          <p
            id="notification-template-help"
            class="mt-1.5 text-xs text-muted"
          >
            $host, $job, $status, $output, $output:10 (last 10 lines), $name (named secret), $$
            (literal dollar). In JSON, place variables inside strings. GET requires an empty
            plain-text body.
          </p>
        </div>
        <div class="my-4 space-y-3">
          <label class="flex items-center gap-2 text-sm">
            <input
              v-model="form.enabled"
              type="checkbox"
              class="size-4 shrink-0 accent-accent"
            />
            Enabled
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input
              v-model="form.defaultEnabled"
              type="checkbox"
              class="size-4 shrink-0 accent-accent"
            />
            On by default for new jobs
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
          :disabled="busy"
        >
          <LoaderCircle
            v-if="busy"
            :size="17"
            class="motion-safe:animate-spin"
          />
          <template v-if="busy">Saving&hellip;</template>
          <template v-else>{{ mode === 'delete' ? 'Remove target' : 'Save target' }}</template>
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
