<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { createTerminalLayout } from './terminal-layout.js';

const props = defineProps({
  text: { type: String, required: true },
  following: { type: Boolean, default: true },
});
const element = ref(null);
const columns = ref(120);
const lineHeight = ref(19.5);
const viewportHeight = ref(400);
const scrollTop = ref(0);
const revision = ref(0);
const layout = createTerminalLayout();
let observer;
let context;
let mounted = false;
let characterWidth = 7.2;
const glyphWidths = new Map();
const rowCount = computed(() => {
  void revision.value;
  return layout.count;
});
// Browsers cap element height. Scale the scrollbar for exceptionally long logs,
// while the visible rows keep their normal font size and reading position.
const contentHeight = computed(() => Math.min(8000000, rowCount.value * lineHeight.value));
const scale = computed(() =>
  Math.max(
    1,
    (rowCount.value * lineHeight.value + 24 - viewportHeight.value) /
      Math.max(1, contentHeight.value + 24 - viewportHeight.value),
  ),
);
const first = computed(() =>
  Math.max(0, Math.floor((scrollTop.value * scale.value - 12) / lineHeight.value) - 30),
);
const visible = computed(() => {
  void revision.value;
  return layout.window(first.value, Math.ceil(viewportHeight.value / lineHeight.value) + 60);
});

watch(
  () => [props.text, columns.value],
  async () => {
    const terminal = element.value;
    const physicalTop = terminal?.scrollTop || 0;
    const previousScale = scale.value;
    const previousTop = physicalTop * previousScale;
    layout.update(props.text, columns.value, measureGlyph);
    revision.value++;
    await nextTick();
    if (mounted && terminal === element.value && terminal) {
      if (props.following) {
        terminal.scrollTop = terminal.scrollHeight;
      } else if (previousScale !== scale.value && terminal.scrollTop === physicalTop) {
        terminal.scrollTop = previousTop / scale.value;
      }
      scrollTop.value = terminal.scrollTop;
    }
  },
  { immediate: true, flush: 'sync' },
);

onMounted(() => {
  mounted = true;
  context = document.createElement('canvas').getContext('2d');
  observer = new window.ResizeObserver(measure);
  observer.observe(element.value);
  measure();
});
onUnmounted(() => {
  mounted = false;
  observer?.disconnect();
});
defineExpose({ element });

function measure() {
  const terminal = element.value;
  const style = window.getComputedStyle(terminal);
  if (context) {
    context.font = `${style.fontSize} ${style.fontFamily}`;
    characterWidth = context.measureText('0').width || 7.2;
    glyphWidths.clear();
  }
  columns.value = Math.max(1, Math.floor((terminal.clientWidth - 24) / characterWidth));
  lineHeight.value = Number.parseFloat(style.lineHeight) || 19.5;
  viewportHeight.value = terminal.clientHeight;
}

function measureGlyph(segment) {
  if (!context) {
    return 1;
  }
  if (glyphWidths.has(segment)) {
    return glyphWidths.get(segment);
  }
  const width = Math.max(0, context.measureText(segment).width / characterWidth);
  if (glyphWidths.size < 1024) {
    glyphWidths.set(segment, width);
  }
  return width;
}

function onKey(event) {
  const movement = {
    ArrowUp: -lineHeight.value,
    ArrowDown: lineHeight.value,
    PageUp: -viewportHeight.value,
    PageDown: viewportHeight.value,
  }[event.key];
  if (movement === undefined && event.key !== 'Home' && event.key !== 'End') {
    return;
  }
  event.preventDefault();
  const terminal = element.value;
  if (event.key === 'Home') {
    terminal.scrollTop = 0;
  } else if (event.key === 'End') {
    terminal.scrollTop = terminal.scrollHeight;
  } else {
    terminal.scrollTop += movement / scale.value;
  }
  scrollTop.value = terminal.scrollTop;
}
</script>

<template>
  <div
    ref="element"
    tabindex="0"
    aria-label="Host terminal output"
    class="h-full max-w-full overflow-auto rounded-md bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100 [overflow-anchor:none]"
    @scroll.passive="scrollTop = $event.target.scrollTop"
    @keydown="onKey"
  >
    <div
      class="relative"
      :style="{ height: `${contentHeight}px` }"
    >
      <pre
        class="absolute m-0 font-mono text-xs leading-relaxed whitespace-pre"
        :style="{ top: `${first * lineHeight - scrollTop * (scale - 1)}px` }"
        >{{ visible }}</pre>
    </div>
  </div>
</template>
