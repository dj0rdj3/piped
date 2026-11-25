<script setup>
import { Plus, Trash2 } from '@lucide/vue';
import AppButton from './AppButton.vue';
import FormInput from './FormInput.vue';

defineProps({
  idPrefix: { type: String, required: true },
  namePattern: { type: String, default: '[A-Za-z_][A-Za-z0-9_]*' },
  namePlaceholder: { type: String, default: 'token' },
  description: { type: String, required: true },
});

const rows = defineModel({ type: Array, required: true });
let nextKey = rows.value.length;

function addSecret() {
  rows.value.push({ key: nextKey++, name: '', value: '', stored: false });
}
</script>

<template>
  <fieldset class="my-4 min-w-0 border-0 p-0">
    <legend
      class="mb-2 flex w-full items-center gap-3 px-0 text-sm font-[550] after:h-px after:flex-1 after:bg-border"
    >
      Secrets
    </legend>
    <p class="text-xs text-muted">
      {{ description }}
    </p>
    <div
      v-for="(secret, index) in rows"
      :key="secret.key"
      class="mt-3 grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-2 max-[560px]:grid-cols-[minmax(0,1fr)_auto]"
    >
      <div class="min-w-0">
        <label
          :for="`${idPrefix}-name-${secret.key}`"
          class="mb-1.5 block text-xs text-muted"
        >
          Name
        </label>
        <FormInput
          :id="`${idPrefix}-name-${secret.key}`"
          v-model="secret.name"
          :aria-label="`Secret ${index + 1} name`"
          :readonly="secret.stored"
          required
          :pattern="namePattern"
          maxlength="64"
          autocomplete="off"
          spellcheck="false"
          :placeholder="namePlaceholder"
        />
      </div>
      <div class="min-w-0 max-[560px]:col-start-1 max-[560px]:row-start-auto">
        <label
          :for="`${idPrefix}-value-${secret.key}`"
          class="mb-1.5 block text-xs text-muted"
        >
          {{ secret.stored ? 'Replace value' : 'Value' }}
        </label>
        <FormInput
          :id="`${idPrefix}-value-${secret.key}`"
          v-model="secret.value"
          :aria-label="`Secret ${index + 1} value`"
          type="password"
          :required="!secret.stored"
          maxlength="4096"
          autocomplete="new-password"
          :placeholder="secret.stored ? 'Leave empty to keep' : ''"
        />
      </div>
      <AppButton
        variant="icon"
        danger
        class="mb-1.5 max-[560px]:col-start-2 max-[560px]:row-span-2"
        :aria-label="`Remove secret ${secret.name || index + 1}`"
        @click="rows = rows.filter((row) => row.key !== secret.key)"
      >
        <Trash2 :size="17" />
      </AppButton>
    </div>
    <AppButton
      class="mt-3"
      :disabled="rows.length >= 16"
      @click="addSecret"
    >
      <Plus :size="17" />
      Add secret
    </AppButton>
  </fieldset>
</template>
