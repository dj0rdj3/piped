<script setup>
defineOptions({ inheritAttrs: false });
defineProps({ multiline: { type: Boolean, default: false } });
const model = defineModel({ type: String, default: '' });
</script>

<template>
  <textarea
    v-if="multiline"
    v-bind="$attrs"
    v-model="model"
    class="min-h-[2.9rem] w-full resize-y rounded-lg border border-border-strong bg-surface px-3.5 py-3 font-mono text-[.8rem] text-foreground transition-colors placeholder:text-sm placeholder:text-muted focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring motion-reduce:transition-none [overflow-wrap:anywhere]"
  />
  <input
    v-else
    v-bind="$attrs"
    v-model="model"
    class="min-h-[2.9rem] w-full rounded-lg border border-border-strong bg-surface px-3.5 py-3 text-foreground transition-colors placeholder:text-sm placeholder:text-muted focus:border-focus focus:outline-none focus:ring-3 focus:ring-focus-ring motion-reduce:transition-none"
  />
</template>
