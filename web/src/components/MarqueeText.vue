<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue';

// MarqueeText — truncates with ellipsis by default, and when the text is
// wider than its container it slides the full string into view on hover.
// The shift distance is measured from the DOM (scrollWidth − clientWidth)
// so the end of the string lands flush with the right edge rather than
// over- or under-running.
const props = defineProps<{
  text: string;
  // Slide duration scales with overflow distance so a 400px overflow doesn't
  // take the same time as a 40px overflow. Defaults to 80 px/sec.
  pxPerSec?: number;
}>();

const outer = ref<HTMLElement | null>(null);
const inner = ref<HTMLElement | null>(null);
const shift = ref(0);
const duration = ref(0);
let ro: ResizeObserver | null = null;

function measure(): void {
  if (!outer.value || !inner.value) return;
  const over = inner.value.scrollWidth - outer.value.clientWidth;
  if (over > 1) {
    shift.value = -(over + 4);
    const speed = props.pxPerSec ?? 80;
    duration.value = Math.max(0.8, Math.abs(shift.value) / speed);
  } else {
    shift.value = 0;
    duration.value = 0;
  }
}

onMounted(() => {
  measure();
  requestAnimationFrame(measure);
  if (typeof ResizeObserver !== 'undefined' && outer.value) {
    ro = new ResizeObserver(measure);
    ro.observe(outer.value);
  }
});
onBeforeUnmount(() => {
  ro?.disconnect();
  ro = null;
});

watch(() => props.text, () => requestAnimationFrame(measure));
</script>

<template>
  <div
    ref="outer"
    class="marquee"
    :class="{ scrollable: shift < 0 }"
    :style="{
      '--marquee-shift': shift + 'px',
      '--marquee-duration': duration + 's',
    }"
    :title="text"
  >
    <span ref="inner" class="marquee-inner">{{ text }}</span>
  </div>
</template>

<style scoped>
.marquee {
  display: block;
  overflow: hidden;
  white-space: nowrap;
  position: relative;
  max-width: 100%;
}
/* Fade the right edge when content overflows — visual cue that there's more
   to see without needing a separate "…" marker. */
.marquee.scrollable {
  -webkit-mask-image: linear-gradient(to right, #000 0, #000 calc(100% - 18px), transparent 100%);
  mask-image: linear-gradient(to right, #000 0, #000 calc(100% - 18px), transparent 100%);
}
.marquee.scrollable:hover {
  -webkit-mask-image: none;
  mask-image: none;
}
.marquee-inner {
  display: inline-block;
  will-change: transform;
  transition: transform 0.35s ease;
}
.marquee.scrollable:hover .marquee-inner {
  transform: translateX(var(--marquee-shift));
  transition: transform var(--marquee-duration) linear;
}
</style>
