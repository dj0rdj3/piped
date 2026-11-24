<script setup>
import { Menu } from '@lucide/vue';
import AppButton from './AppButton.vue';

defineProps({
  title: { type: String, required: true },
  titleId: { type: String, required: true },
});
const emit = defineEmits(['open-navigation']);
</script>

<template>
  <div class="mb-5 flex flex-wrap items-center justify-between gap-4">
    <div class="flex min-w-0 items-center gap-3">
      <AppButton
        variant="icon"
        class="min-[781px]:hidden"
        aria-label="Open navigation"
        aria-controls="main-navigation"
        @click="emit('open-navigation')"
      >
        <Menu :size="21" />
      </AppButton>
      <h1
        :id="titleId"
        class="text-[1.8rem] font-[550] tracking-[-.045em] max-[780px]:text-[1.7rem]"
      >
        {{ title }}
      </h1>
    </div>
    <slot />
  </div>
</template>
