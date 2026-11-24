<script setup>
defineProps({
  variant: {
    type: String,
    default: 'secondary',
    validator: (value) => ['primary', 'secondary', 'danger', 'icon', 'text'].includes(value),
  },
  danger: { type: Boolean, default: false },
  type: { type: String, default: 'button' },
});

const variants = {
  primary: 'border-transparent bg-accent text-accent-foreground enabled:hover:bg-accent-hover',
  secondary: 'border-border-strong bg-surface text-foreground enabled:hover:bg-surface-hover',
  danger: 'border-transparent bg-danger text-white enabled:hover:bg-danger-hover',
  icon: 'size-8 shrink-0 rounded-md border-0 bg-transparent p-1.5 text-muted enabled:hover:bg-surface-hover',
  text: 'gap-1.5 border-0 bg-transparent p-0 text-sm text-muted enabled:hover:underline',
};
</script>

<template>
  <button
    :type="type"
    class="inline-flex cursor-pointer items-center justify-center focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4 disabled:cursor-not-allowed disabled:opacity-50"
    :class="[
      variants[variant],
      variant !== 'icon' && variant !== 'text'
        ? 'min-h-[2.7rem] gap-2 rounded-md border px-4 py-2.5 text-sm font-semibold transition-colors motion-reduce:transition-none'
        : '',
      variant === 'icon' && (danger ? 'enabled:hover:text-error' : 'enabled:hover:text-foreground'),
    ]"
  >
    <slot />
  </button>
</template>
