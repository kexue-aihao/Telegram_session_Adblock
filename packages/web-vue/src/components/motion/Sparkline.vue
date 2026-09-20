<script setup lang="ts">
import { computed } from 'vue';

/**
 * 迷你折线。
 *
 * 用 stroke-dasharray 的动画「画出」线条，而不是逐点补间：
 * 前者只需要浏览器插值一条属性，后者要重算整条 path 的 d，
 * 在 90 个点上会明显掉帧。
 */
const props = withDefaults(
  defineProps<{
    points: number[];
    width?: number;
    height?: number;
    tone?: 'accent' | 'success' | 'danger';
  }>(),
  { width: 280, height: 64, tone: 'accent' },
);

const strokeColor = computed(
  () =>
    ({
      accent: 'var(--color-accent)',
      success: 'var(--color-success)',
      danger: 'var(--color-danger)',
    })[props.tone],
);

const geometry = computed(() => {
  const values = props.points;
  if (values.length < 2) return null;

  const max = Math.max(...values, 1);
  const step = props.width / (values.length - 1);
  // 上下各留 2px，避免峰值贴边被裁掉
  const usable = props.height - 4;

  const coords = values.map((v, i) => {
    const x = i * step;
    const y = props.height - 2 - (v / max) * usable;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });

  const line = `M${coords.join(' L')}`;
  const area = `${line} L${props.width},${props.height} L0,${props.height} Z`;
  return { line, area };
});
</script>

<template>
  <svg
    v-if="geometry"
    :viewBox="`0 0 ${width} ${height}`"
    preserveAspectRatio="none"
    :style="{ width: `${width}px`, height: `${height}px` }"
    aria-hidden="true"
  >
    <defs>
      <linearGradient :id="`spark-fill-${tone}`" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" :stop-color="strokeColor" stop-opacity="0.22" />
        <stop offset="100%" :stop-color="strokeColor" stop-opacity="0" />
      </linearGradient>
    </defs>

    <path :d="geometry.area" :fill="`url(#spark-fill-${tone})`" class="spark-area" />
    <path
      :d="geometry.line"
      fill="none"
      :stroke="strokeColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      class="spark-line"
    />
  </svg>
</template>

<style scoped>
/* 画线：从 0 到 1 的 dasharray。
   pathLength 归一化让这个值不依赖实际路径长度。 */
.spark-line {
  stroke-dasharray: 1;
  stroke-dashoffset: 1;
  animation: draw 900ms var(--ease-expo) forwards;
}

@keyframes draw {
  to {
    stroke-dashoffset: 0;
  }
}

.spark-area {
  animation: fade-in var(--duration-layout) var(--ease-expo) 200ms both;
}

@keyframes fade-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

@media (prefers-reduced-motion: reduce) {
  .spark-line {
    stroke-dashoffset: 0;
    animation: none;
  }
}
</style>
