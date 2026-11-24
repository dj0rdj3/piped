<script setup>
import { onMounted, onUnmounted, ref } from 'vue';
import { X } from '@lucide/vue';
import AppButton from './AppButton.vue';
import { createBackdropDismissal } from '../../lib/dialog-backdrop.js';
import { confirmDiscardChanges, registerUnsavedChanges } from '../../lib/unsaved-changes.js';

const props = defineProps({
  title: { type: String, required: true },
  titleId: { type: String, required: true },
  busy: { type: Boolean, default: false },
  wide: { type: Boolean, default: false },
  dirty: { type: Boolean, default: false },
});
const emit = defineEmits(['close']);
const dialog = ref(null);
const backdrop = createBackdropDismissal(requestClose);

let unregister = () => {};
onMounted(() => {
  unregister = registerUnsavedChanges(() => props.dirty);
  dialog.value.showModal();
});
onUnmounted(() => unregister());

function requestClose() {
  if (!props.busy && confirmDiscardChanges()) {
    emit('close');
  }
}
defineExpose({ requestClose });
</script>

<template>
  <dialog
    ref="dialog"
    :aria-labelledby="titleId"
    class="m-auto max-h-[calc(100dvh_-_3rem)] w-[calc(100%_-_2rem)] overflow-y-auto rounded-[.9rem] border border-border bg-surface p-[1.35rem] text-foreground shadow-[0_25px_80px_#0000000a] backdrop:bg-black/70 backdrop:backdrop-blur-[3px] max-[560px]:p-[1.3rem] dark:shadow-[0_25px_80px_#00000030] dark:backdrop:bg-black/80"
    :class="wide ? 'max-w-[36rem]' : 'max-w-[30rem]'"
    @cancel.self.prevent="requestClose"
    @pointerdown="backdrop.pointerDown($event)"
    @pointercancel="backdrop.pointerCancel()"
    @click="backdrop.click($event)"
  >
    <div class="mb-4 flex items-start justify-between">
      <div
        class="inline-flex size-12 items-center justify-center rounded-[.85rem] border border-border bg-surface-muted text-muted"
      >
        <slot name="icon" />
      </div>
      <AppButton
        variant="icon"
        aria-label="Close dialog"
        :disabled="busy"
        @click="requestClose"
      >
        <X :size="20" />
      </AppButton>
    </div>
    <h2
      :id="titleId"
      class="mb-2 text-2xl font-[550] tracking-[-.035em]"
    >
      {{ title }}
    </h2>
    <slot />
  </dialog>
</template>
