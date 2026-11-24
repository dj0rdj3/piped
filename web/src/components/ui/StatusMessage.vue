<script setup>
defineProps({
  variant: {
    type: String,
    default: 'notice',
    validator: (value) => ['notice', 'error'].includes(value),
  },
});
</script>

<template>
  <div
    :role="variant === 'error' ? 'alert' : 'status'"
    class="relative my-5 flex justify-between gap-4 overflow-hidden rounded-lg border border-border bg-surface-muted px-4 py-3.5 text-sm leading-relaxed before:pointer-events-none before:absolute before:inset-y-0 before:start-0 before:w-0.5"
    :class="variant === 'error' ? 'text-error before:bg-error' : 'text-foreground before:bg-accent'"
  >
    <span><slot /></span>
    <slot name="action" />
  </div>
</template>
