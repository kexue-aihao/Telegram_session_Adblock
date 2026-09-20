<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import AppIcon, { type IconName } from '@/components/ui/AppIcon.vue';

/**
 * 命令面板（⌘K）。
 *
 * 玻璃质感在这里用得最重（glass-deep），因为它是浮在一切之上的临时表面 ——
 * 也正是磨砂最能体现价值的地方：背后真的有一整页内容在流动。
 */
const props = defineProps<{
  open: boolean;
  items: { to: string; label: string; icon: IconName; keywords: string }[];
}>();
const emit = defineEmits<{ close: []; select: [to: string] }>();

const query = ref('');
const activeIndex = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);

const results = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) return props.items;
  return props.items.filter(
    (item) =>
      item.label.toLowerCase().includes(keyword) || item.keywords.toLowerCase().includes(keyword),
  );
});

// 每次打开都重置状态，否则会保留上次的搜索词与高亮位置
watch(
  () => props.open,
  async (open) => {
    if (!open) return;
    query.value = '';
    activeIndex.value = 0;
    // 等入场动画开始后再聚焦，避免浏览器在元素还在位移时滚动它
    await nextTick();
    window.setTimeout(() => inputRef.value?.focus(), 40);
  },
);

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    emit('close');
    return;
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault();
    activeIndex.value = Math.min(activeIndex.value + 1, results.value.length - 1);
    return;
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault();
    activeIndex.value = Math.max(activeIndex.value - 1, 0);
    return;
  }
  if (event.key === 'Enter') {
    const target = results.value[activeIndex.value];
    if (target) {
      event.preventDefault();
      emit('select', target.to);
    }
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="palette">
      <div v-if="open" class="fixed inset-0 z-50">
        <div class="absolute inset-0 bg-black/50 backdrop-blur-[2px]" @click="emit('close')" />

        <div class="relative flex justify-center px-4" style="padding-top: 18vh">
          <div
            class="glass-deep w-full overflow-hidden rounded-2xl"
            style="max-width: 28rem"
            role="dialog"
            aria-modal="true"
            aria-label="命令面板"
          >
            <div class="flex items-center gap-2.5 border-b border-[var(--color-line-faint)] px-3.5">
              <AppIcon name="search" :size="16" class="shrink-0 text-[var(--color-ink-subtle)]" />
              <input
                ref="inputRef"
                v-model="query"
                placeholder="跳转到…"
                class="h-11 flex-1 bg-transparent text-sm text-[var(--color-ink)] placeholder:text-[var(--color-ink-faint)] focus:outline-none"
                @keydown="onKeydown"
                @input="activeIndex = 0"
              />
              <kbd
                class="rounded border border-[var(--color-line-faint)] px-1.5 py-0.5 font-mono text-2xs text-[var(--color-ink-subtle)]"
              >
                Esc
              </kbd>
            </div>

            <div class="max-h-72 overflow-y-auto p-1.5">
              <p v-if="results.length === 0" class="px-3 py-6 text-center text-xs text-[var(--color-ink-subtle)]">
                没有匹配的结果
              </p>

              <!-- 结果行错峰入场：从下方 4px 淡入，延迟按索引递增。
                   超过 6 行之后不再累加延迟，否则最后一行要等很久。 -->
              <button
                v-for="(item, index) in results"
                :key="item.to"
                type="button"
                class="flex w-full items-center gap-2.5 rounded-xl px-2.5 py-2 text-left text-sm transition-colors duration-[var(--duration-micro)]"
                :class="
                  index === activeIndex
                    ? 'bg-[var(--color-active)] text-[var(--color-ink)]'
                    : 'text-[var(--color-ink-muted)]'
                "
                :style="{
                  animation: `palette-row var(--duration-state) var(--ease-expo) ${Math.min(index, 6) * 25}ms both`,
                }"
                @click="emit('select', item.to)"
                @mouseenter="activeIndex = index"
              >
                <span class="text-[var(--color-ink-subtle)]">
                  <AppIcon :name="item.icon" :size="16" />
                </span>
                <span class="flex-1">{{ item.label }}</span>
                <span class="font-mono text-2xs text-[var(--color-ink-faint)]">{{ item.to }}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
@keyframes palette-row {
  from {
    opacity: 0;
    transform: translateY(4px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* 面板从 scale .96 展开 —— 配合 ⌘K 这种高频动作，
   弹性尺度要克制，太夸张会显得轻浮。 */
.palette-enter-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.palette-enter-active .glass-deep {
  transition: transform var(--duration-layout) var(--ease-spring);
}
.palette-leave-active {
  transition: opacity var(--duration-micro) ease;
}
.palette-enter-from,
.palette-leave-to {
  opacity: 0;
}
.palette-enter-from .glass-deep {
  transform: scale(0.96) translateY(-6px);
}
</style>
