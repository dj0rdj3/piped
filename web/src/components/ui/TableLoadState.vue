<script setup>
import { LoaderCircle } from '@lucide/vue';
import AppButton from './AppButton.vue';

defineProps({ loading: Boolean, error: { type: String, default: '' } });
const emit = defineEmits(['retry']);
</script>

<template>
  <div
    class="flex min-h-16 items-center justify-center gap-2 border-t border-border px-4 py-3 text-sm text-muted"
  >
    <span
      v-if="loading"
      role="status"
      class="flex items-center gap-2"
    >
      <LoaderCircle
        :size="20"
        class="motion-safe:animate-spin"
      />
      Loading&hellip;
    </span>
    <template v-else-if="error">
      <span role="alert">{{ error }}</span>
      <AppButton
        variant="text"
        @click="emit('retry')"
      >
        Retry
      </AppButton>
    </template>
  </div>
</template>
