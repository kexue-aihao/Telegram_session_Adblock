<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { springNumber } from '@/lib/motion';

/**
 * 弹簧补间的数字。
 *
 * 用 requestAnimationFrame 手工积分，而不是 CSS transition：
 * 数字在快速变化时（比如实时统计）需要加速度与惯性，
 * 看起来才像「仪表」而不是「幻灯片」。
 *
 * 直接写 DOM 的 textContent 而不是走响应式 —— 后者会在动画的每一帧
 * 触发一次组件重渲染，而这个组件在仪表盘上有 4 个实例同时跑。
 */
const props = withDefaults(
  defineProps<{ value: number; format?: (v: number) => string; pulse?: boolean }>(),
  { format: (v: number) => String(Math.round(v)), pulse: true },
);

const el = ref<HTMLSpanElement | null>(null);
let stop: (() => void) | null = null;
let displayed = props.value;
let pulseTimer: number | null = null;
let firstRender = true;

function render(value: number) {
  displayed = value;
  if (el.value) el.value.textContent = props.format(value);
}

/**
 * 数值变化时的一次高亮脉冲。
 *
 * 只补间数字是不够的：一个从 12 变成 13 的计数在视觉上几乎是静止的，
 * 用户感觉不到「它刚刚动了」。一次 900ms 的颜色脉冲解决这个问题，
 * 而且比让数字本身闪动要克制得多。
 *
 * 首次渲染不脉冲 —— 打开页面时四个数字同时闪一下很吵。
 */
function triggerPulse() {
  if (!props.pulse || firstRender || !el.value) return;

  el.value.classList.remove('value-pulse');
  // 强制重排让动画能重新触发：连续两次变化时，
  // 不重排的话第二次不会重新播放（class 没变）
  void el.value.offsetWidth;
  el.value.classList.add('value-pulse');

  if (pulseTimer !== null) window.clearTimeout(pulseTimer);
  pulseTimer = window.setTimeout(() => {
    el.value?.classList.remove('value-pulse');
    pulseTimer = null;
  }, 900);
}

/**
 * 首次落位必须在 onMounted 里做。
 *
 * setup 期间 `el` 还是 null（模板尚未挂载），那时写 textContent 等于
 * 什么都没发生 —— 表现为「数字整块不显示」，而且不报任何错。
 * 首次不做动画：首屏上四个数字同时从 0 滚上来很闹。
 */
onMounted(() => {
  render(props.value);
  // 让首次的 watch 回调知道自己不是第一次
  requestAnimationFrame(() => {
    firstRender = false;
  });
});

watch(
  () => props.value,
  (next) => {
    stop?.();
    stop = springNumber(displayed, next, render);
    triggerPulse();
  },
);

onBeforeUnmount(() => {
  stop?.();
  if (pulseTimer !== null) window.clearTimeout(pulseTimer);
});
</script>

<template>
  <span ref="el" class="tnum" />
</template>
