<script setup lang="ts">
import { ref } from 'vue';

/**
 * 卡片。
 *
 * 三处细节各自解决一个具体问题：
 *
 *  1. **悬停上浮 2px + 提亮描边** —— 位移很小，但足以让「这张卡可点」
 *     变得明确。用阴影表达可点性在暗色下会糊成一团脏色。
 *
 *  2. **光标跟随的聚光** —— 鼠标在卡面上移动时，一层极淡的径向高光
 *     跟着走。它给静态的卡片一个「有光从上面照下来」的物理感，
 *     是整个界面里最便宜也最有效的质感来源之一。
 *     强度刻意压得很低（8%）：看得见的是「质感」，看得清的是「特效」，
 *     后者是廉价的。
 *
 *  3. **只在指针设备上启用** —— 触屏没有 hover 语义，
 *     跟随一个不存在的光标只会白白消耗算力。
 */
withDefaults(defineProps<{ interactive?: boolean; flush?: boolean; spotlight?: boolean }>(), {
  interactive: false,
  flush: false,
  spotlight: true,
});

const el = ref<HTMLElement | null>(null);
const spot = ref({ x: 0, y: 0, on: false });

/**
 * 用 getBoundingClientRect 换算坐标，而不是读 offsetX/offsetY：
 * 后者是相对**事件目标**的，而鼠标经常落在卡片的子元素上 ——
 * 那样光斑会在每个子元素边界上跳一下。
 */
function onMove(event: MouseEvent) {
  const node = el.value;
  if (!node) return;
  const rect = node.getBoundingClientRect();
  spot.value = {
    x: event.clientX - rect.left,
    y: event.clientY - rect.top,
    on: true,
  };
}

function onLeave() {
  spot.value = { ...spot.value, on: false };
}
</script>

<template>
  <div
    ref="el"
    class="surface group relative overflow-hidden rounded-2xl"
    :class="[
      interactive &&
        'transition-all duration-[var(--duration-state)] ease-[var(--ease-expo)] hover:-translate-y-0.5 hover:border-[var(--color-line-strong)]',
      !flush && 'p-4',
    ]"
    @mousemove="spotlight && onMove($event)"
    @mouseleave="onLeave"
  >
    <!-- 聚光层。用 transition 淡入淡出而不是直接跟随 ——
         光斑瞬间出现会像一块贴上去的色块。 -->
    <div
      v-if="spotlight"
      class="pointer-events-none absolute inset-0 rounded-2xl transition-opacity duration-[var(--duration-state)]"
      :style="{
        opacity: spot.on ? 1 : 0,
        background: `radial-gradient(240px circle at ${spot.x}px ${spot.y}px, var(--spot-color), transparent 70%)`,
      }"
      aria-hidden="true"
    />
    <slot />
  </div>
</template>
