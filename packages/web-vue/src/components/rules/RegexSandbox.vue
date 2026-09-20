<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { api, ApiError } from '@/lib/api';
import { splitHighlight } from '@/lib/format';
import type { RuleTestResult } from '@/lib/types';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppTextarea from '@/components/ui/AppTextarea.vue';

/**
 * 正则测试沙盒。
 *
 * 三个设计要点：
 *
 *  1. **自动跑，不需要点「测试」按钮。** 调正则是一个反复试错的过程，
 *     每改一次都点一下按钮会非常烦。防抖 400ms。
 *  2. **命中片段在原文里高亮**，而不是只列出匹配到的字符串 ——
 *     看到它在上下文里的位置才判断得出这条规则会不会误伤。
 *  3. **同时显示归一化后的文本。** 管理员最常问的就是「我写的是加微信，
 *     为什么『加<零宽>微<零宽>信』也被拦了」，把归一化结果摆出来，
 *     这个问题就不言自明。
 */
const props = defineProps<{
  pattern: string;
  flags: string;
  matchMode: string;
}>();
const emit = defineEmits<{
  'update:pattern': [value: string];
  'update:flags': [value: string];
}>();

const sample = ref('你好，加微信详聊 vx: abc123，或访问 https://t.me/example');
const result = ref<RuleTestResult | null>(null);
const error = ref<string | null>(null);
const running = ref(false);

let debounceTimer: number | null = null;
let generation = 0;

function schedule() {
  if (debounceTimer !== null) window.clearTimeout(debounceTimer);
  debounceTimer = window.setTimeout(() => void run(), 400);
}

async function run() {
  if (!props.pattern) {
    result.value = null;
    error.value = null;
    return;
  }

  const current = ++generation;
  running.value = true;

  try {
    const response = await api.post<RuleTestResult>('/api/rules/test', {
      pattern: props.pattern,
      flags: props.flags || 'iu',
      matchMode: props.matchMode || 'regex',
      sample: sample.value,
    });
    // 丢弃过期响应：慢请求回来时覆盖掉新结果会让高亮跳到旧位置
    if (current !== generation) return;
    result.value = response;
    error.value = null;
  } catch (err) {
    if (current !== generation) return;
    result.value = null;
    error.value = err instanceof ApiError ? err.message : '测试失败';
  } finally {
    if (current === generation) running.value = false;
  }
}

watch(() => [props.pattern, props.flags, props.matchMode], schedule, { immediate: true });
watch(sample, schedule);

const highlighted = computed(() => {
  const r = result.value;
  if (!r || r.matches.length === 0) return null;
  return splitHighlight(r.normalizedSample ?? sample.value, r.matches);
});

const hasError = computed(() => error.value !== null || result.value?.timedOut === true);
</script>

<template>
  <section class="space-y-3 rounded-2xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] p-3.5">
    <div class="flex items-center justify-between gap-3">
      <div>
        <p class="text-xs font-medium">测试沙盒</p>
        <p class="text-2xs text-[var(--color-ink-subtle)]">
          与线上完全相同的匹配引擎与归一化流程
        </p>
      </div>
      <span v-if="running" class="text-2xs text-[var(--color-ink-subtle)]">匹配中…</span>
      <AppBadge
        v-else-if="result"
        :tone="result.matches.length > 0 ? 'success' : 'neutral'"
      >
        {{ result.matches.length > 0 ? `命中 ${result.matches.length} 处 · ${result.durationMs}ms` : '未命中' }}
      </AppBadge>
    </div>

    <!-- 模式输入：左右两端的 / 是装饰，让「这是正则」这件事一眼可见 -->
    <div class="flex gap-2">
      <div class="relative flex-1">
        <span
          class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-xs text-[var(--color-ink-faint)]"
        >
          /
        </span>
        <AppInput
          :model-value="props.pattern"
          placeholder="加\s*(微信|vx|QQ)"
          spellcheck="false"
          class="pr-10 pl-5 font-mono text-xs"
          :invalid="hasError"
          @update:model-value="emit('update:pattern', $event)"
        />
        <span
          class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 font-mono text-xs text-[var(--color-ink-faint)]"
        >
          /{{ props.flags }}
        </span>
      </div>

      <AppInput
        :model-value="props.flags"
        class="w-20 text-center font-mono text-xs"
        spellcheck="false"
        aria-label="正则标志"
        title="允许的标志：g i m s u y"
        @update:model-value="emit('update:flags', $event.replace(/[^gimsuy]/g, ''))"
      />
    </div>

    <Transition name="err">
      <p
        v-if="hasError"
        class="flex items-start gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]"
      >
        <AppIcon name="warning" :size="14" class="mt-px shrink-0" />
        {{ error ?? '匹配超时 —— 这条正则可能过于宽泛，请改写为更具体的模式。' }}
      </p>
    </Transition>

    <div class="space-y-1.5">
      <label class="text-2xs font-medium text-[var(--color-ink-muted)]">样本文本</label>
      <AppTextarea
        v-model="sample"
        :rows="3"
        spellcheck="false"
        class="text-xs"
        placeholder="粘贴一段真实的广告文案，看这条规则会不会命中它"
      />
    </div>

    <Transition name="err">
      <div v-if="highlighted" class="space-y-2">
        <div class="space-y-1">
          <p class="text-2xs font-medium text-[var(--color-ink-muted)]">
            归一化后的文本（规则实际匹配的就是它）
          </p>
          <p class="rounded-lg bg-[var(--color-bg-1)] px-2.5 py-2 text-xs leading-relaxed break-words">
            <template v-for="(part, i) in highlighted" :key="i">
              <mark
                v-if="part.hit"
                class="rounded bg-[var(--color-danger-soft)] px-0.5 text-[var(--color-danger)]"
              >
                {{ part.text }}
              </mark>
              <span v-else class="text-[var(--color-ink-muted)]">{{ part.text }}</span>
            </template>
          </p>
        </div>

        <div v-if="result?.matches.some((m) => m.groups.length > 0)" class="space-y-1">
          <p class="text-2xs font-medium text-[var(--color-ink-muted)]">捕获组</p>
          <div class="space-y-1">
            <div
              v-for="(m, i) in result.matches.slice(0, 5)"
              :key="i"
              class="flex flex-wrap items-center gap-1.5"
            >
              <code class="font-mono text-2xs text-[var(--color-ink-subtle)]">#{{ i + 1 }}</code>
              <AppBadge v-for="(g, gi) in m.groups" v-show="g !== null" :key="gi" tone="accent">
                ${{ gi + 1 }} = {{ g }}
              </AppBadge>
            </div>
          </div>
        </div>

        <p v-if="result?.ok" class="flex items-center gap-1.5 text-2xs text-[var(--color-success)]">
          <AppIcon name="check" :size="14" />
          这条规则通过了编译检查，可以保存
        </p>
      </div>
    </Transition>

    <!-- 不能写成 v-else-if：它必须紧跟在 v-if 兄弟节点后面，
         而中间隔着一个 <Transition> 包装，Vue 编译期就会报
         「v-else-if has no adjacent v-if」。这里用完整条件显式表达。 -->
    <p
      v-if="!highlighted && result && props.pattern && !hasError && result.matches.length === 0"
      class="text-2xs text-[var(--color-ink-subtle)]"
    >
      这条规则没有命中上面的样本。如果样本里确实有该拦的内容，可以放宽模式
      —— 比如用 <code class="font-mono text-[var(--color-ink-muted)]">\s*</code> 允许词之间夹空格。
    </p>
  </section>
</template>

<style scoped>
.err-enter-active {
  transition:
    opacity var(--duration-state) var(--ease-state),
    transform var(--duration-state) var(--ease-expo);
}
.err-enter-from {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
