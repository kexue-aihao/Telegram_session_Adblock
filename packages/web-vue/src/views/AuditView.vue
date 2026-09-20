<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '@/lib/api';
import { formatDateTime, relativeTime, OUTCOME_LABELS } from '@/lib/format';
import type { AuditEntry, RuleHit } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useDebouncedRef } from '@/composables/useDebounced';
import { useWsEvent } from '@/composables/useWsEvent';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppCard from '@/components/ui/AppCard.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppSelect from '@/components/ui/AppSelect.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';

/**
 * 审计页。
 *
 * 两个标签：「广告命中」是机器人的工作日志（量极大，重在可筛选与可解释），
 * 「面板操作」是谁改了什么（量小，重在可追溯）。
 */
const tab = ref<'hits' | 'log'>('hits');
const query = ref('');
const debouncedQuery = useDebouncedRef(query, 300);
const action = ref('');

const hits = useAsync<{ items: RuleHit[]; total: number | null }>(
  () => api.get('/api/audit/hits', { q: debouncedQuery.value || undefined, limit: 30 }),
  () => [debouncedQuery.value],
);

const logs = useAsync<{ items: AuditEntry[]; total: number | null }>(
  () => api.get('/api/audit/log', { q: debouncedQuery.value || undefined, action: action.value || undefined, limit: 50 }),
  () => [debouncedQuery.value, action.value],
);

const actions = useAsync<{ items: string[] }>(() => api.get('/api/audit/actions'));

useWsEvent('rule.hit', () => {
  if (tab.value === 'hits') void hits.reload();
});
useWsEvent('audit.new', () => {
  if (tab.value === 'log') void logs.reload();
});

const expanded = ref<Set<number>>(new Set());

function toggleExpanded(id: number) {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

/**
 * 规则快照怎么展示。
 *
 * 共现规则的 pattern 是阈值整数而不是正则，套上 `/…/flags` 会被读成
 * 「匹配字面量 3 的正则」。只对共现改说法，其余保持原样 —— 老记录
 * 没有这一列（回填的是 'regex'），展示与从前完全一致。
 */
const ruleSnapshot = (hit: RuleHit) =>
  hit.ruleMatchMode === 'cooccurrence'
    ? `同一条消息命中 ${hit.rulePattern} 条不同规则时触发`
    : `/${hit.rulePattern}/${hit.ruleFlags}`;

const stagger = (index: number) => ({
  animation: `hit-in var(--duration-layout) var(--ease-expo) ${Math.min(index, 12) * 30}ms both`,
});

const totalHits = computed(() => hits.data.value?.total ?? 0);
const totalLogs = computed(() => logs.data.value?.total ?? 0);
</script>

<template>
  <div class="mx-auto w-full max-w-[1100px] space-y-4 p-5 lg:p-6">
    <div class="space-y-0.5">
      <h2 class="text-lg font-semibold tracking-tight">审计</h2>
      <p class="max-w-2xl text-xs leading-relaxed text-[var(--color-ink-muted)]">
        命中记录保存了规则的完整快照，因此即使规则后来被改或被删，这里依然能解释
        「当时是按什么判定的」。
      </p>
    </div>

    <!-- 标签栏：指示条用绝对定位的一条，切换时有横向滑动 -->
    <div class="flex items-center gap-1 border-b border-[var(--color-line-faint)]">
      <button
        v-for="t in [
          { key: 'hits' as const, label: '广告命中', count: totalHits },
          { key: 'log' as const, label: '面板操作', count: totalLogs },
        ]"
        :key="t.key"
        type="button"
        class="relative px-3.5 py-2.5 text-sm transition-colors duration-[var(--duration-micro)]"
        :class="tab === t.key ? 'text-[var(--color-ink)]' : 'text-[var(--color-ink-muted)] hover:text-[var(--color-ink)]'"
        @click="tab = t.key"
      >
        {{ t.label }}
        <span v-if="t.count > 0" class="ml-1.5 text-2xs text-[var(--color-ink-subtle)]">{{ t.count }}</span>
        <span
          v-if="tab === t.key"
          class="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-[var(--color-accent)]"
        />
      </button>
    </div>

    <!-- 筛选 -->
    <div class="flex flex-wrap items-center gap-2">
      <AppInput
        v-model="query"
        :placeholder="tab === 'hits' ? '搜索命中内容、规则名或用户名' : '搜索操作、操作者或目标'"
        class="h-8 max-w-xs text-xs"
      />
      <AppSelect v-if="tab === 'log'" v-model="action" class="h-8 w-52 text-xs">
        <option value="">全部操作</option>
        <option v-for="a in actions.data.value?.items" :key="a" :value="a">{{ a }}</option>
      </AppSelect>
    </div>

    <!-- 命中记录 -->
    <template v-if="tab === 'hits'">
      <div v-if="hits.loading.value && !hits.data.value" class="space-y-2">
        <AppSkeleton v-for="i in 5" :key="i" height="5rem" radius="var(--radius-2xl)" />
      </div>

      <AppCard v-else-if="(hits.data.value?.items.length ?? 0) === 0" flush>
        <AppEmpty
          icon="audit"
          :title="query ? '没有匹配的命中记录' : '还没有命中记录'"
          :description="query ? '换个关键词或清空筛选试试。' : '当有用户发送触发规则的内容时，这里会实时出现记录。'"
        />
      </AppCard>

      <div v-else class="space-y-2">
        <AppCard v-for="(hit, index) in hits.data.value?.items" :key="hit.id" :style="stagger(index)">
          <button
            type="button"
            class="flex w-full items-start justify-between gap-3 text-left"
            @click="toggleExpanded(hit.id)"
          >
            <div class="min-w-0 flex-1 space-y-1.5">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-sm font-medium">{{ hit.ruleName }}</span>
                <AppBadge
                  v-for="outcome in hit.outcomes"
                  :key="outcome"
                  :tone="
                    outcome === 'delete_failed' || outcome === 'regex_timeout' || outcome === 'notify_failed'
                      ? 'danger'
                      : outcome === 'logged_only'
                        ? 'neutral'
                        : 'success'
                  "
                >
                  {{ OUTCOME_LABELS[outcome] ?? outcome }}
                </AppBadge>
                <AppBadge v-if="hit.severity > 0" tone="warn">+{{ hit.severity }} 分</AppBadge>
              </div>

              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-ink-subtle)]">
                <span>{{ hit.contactName }}<template v-if="hit.contactUsername"> (@{{ hit.contactUsername }})</template></span>
                <span>{{ hit.botName }}</span>
                <span>{{ relativeTime(hit.createdAt) }}</span>
              </div>

              <code
                v-if="hit.matchedText"
                class="block truncate rounded-lg bg-[var(--color-bg-2)] px-2 py-1.5 font-mono text-xs text-[var(--color-danger)]"
              >
                {{ hit.matchedText }}
              </code>
            </div>

            <AppIcon
              name="chevron-down"
              :size="16"
              class="mt-0.5 shrink-0 text-[var(--color-ink-subtle)] transition-transform duration-[var(--duration-state)]"
              :style="{ transform: expanded.has(hit.id) ? 'rotate(180deg)' : 'rotate(0)' }"
            />
          </button>

          <Transition name="expand">
            <div
              v-if="expanded.has(hit.id)"
              class="mt-3 space-y-2.5 border-t border-[var(--color-line-faint)] pt-3"
            >
              <div class="grid grid-cols-[92px_1fr] gap-3">
                <span class="text-2xs text-[var(--color-ink-subtle)]">规则快照</span>
                <code class="min-w-0 font-mono text-xs break-all text-[var(--color-ink-muted)]">
                  {{ ruleSnapshot(hit) }}
                </code>
              </div>

              <div v-if="hit.normalizedExcerpt" class="grid grid-cols-[92px_1fr] gap-3">
                <span class="text-2xs text-[var(--color-ink-subtle)]">归一化上下文</span>
                <code class="min-w-0 font-mono text-xs break-all text-[var(--color-ink-muted)]">
                  {{ hit.normalizedExcerpt }}
                </code>
              </div>

              <div class="grid grid-cols-[92px_1fr] gap-3">
                <span class="text-2xs text-[var(--color-ink-subtle)]">用户</span>
                <span class="text-xs text-[var(--color-ink-muted)]">
                  {{ hit.contactName }} · TG ID <code class="font-mono">{{ hit.tgUserId }}</code>
                </span>
              </div>

              <div class="grid grid-cols-[92px_1fr] gap-3">
                <span class="text-2xs text-[var(--color-ink-subtle)]">发生时间</span>
                <span class="text-xs text-[var(--color-ink-muted)]">{{ formatDateTime(hit.createdAt) }}</span>
              </div>

              <p
                v-if="hit.outcomes.includes('delete_failed') || hit.outcomes.includes('regex_timeout')"
                class="flex items-start gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]"
              >
                <AppIcon name="warning" :size="14" class="mt-px shrink-0" />
                {{
                  hit.outcomes.includes('regex_timeout')
                    ? '这条规则在匹配时异常，已被引擎自动停用。请到「规则」页改写它。'
                    : '删除消息失败。通常是超过 Telegram 的 48 小时删除窗口，或机器人缺少「删除消息」权限。'
                }}
              </p>
            </div>
          </Transition>
        </AppCard>
      </div>
    </template>

    <!-- 操作日志 -->
    <template v-else>
      <AppCard flush class="overflow-hidden">
        <div v-if="logs.loading.value && !logs.data.value" class="space-y-2 p-4">
          <AppSkeleton v-for="i in 5" :key="i" height="2.5rem" />
        </div>

        <AppEmpty
          v-else-if="(logs.data.value?.items.length ?? 0) === 0"
          icon="audit"
          compact
          title="没有匹配的操作记录"
          description="登录、修改规则、拉黑用户等操作都会记录在这里。"
        />

        <div
          v-else
          v-for="entry in logs.data.value?.items"
          :key="entry.id"
          class="border-b border-[var(--color-line-faint)] px-4 py-3 last:border-b-0"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 space-y-1">
              <div class="flex flex-wrap items-center gap-2">
                <AppBadge :tone="entry.actorType === 'admin' ? 'accent' : 'neutral'">
                  {{ entry.actorId ?? entry.actorType }}
                </AppBadge>
                <code class="font-mono text-xs text-[var(--color-ink)]">{{ entry.action }}</code>
                <span v-if="entry.targetType" class="text-2xs text-[var(--color-ink-subtle)]">
                  {{ entry.targetType }}<template v-if="entry.targetId"> #{{ entry.targetId }}</template>
                </span>
              </div>
              <code
                v-if="entry.detail && Object.keys(entry.detail).length > 0"
                class="block truncate font-mono text-xs text-[var(--color-ink-subtle)]"
              >
                {{ JSON.stringify(entry.detail) }}
              </code>
            </div>

            <div class="shrink-0 text-right">
              <p class="text-2xs text-[var(--color-ink-subtle)]">{{ relativeTime(entry.createdAt) }}</p>
              <code v-if="entry.ip" class="font-mono text-2xs text-[var(--color-ink-faint)]">{{ entry.ip }}</code>
            </div>
          </div>
        </div>
      </AppCard>
    </template>
  </div>
</template>

<style scoped>
@keyframes hit-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* 行内展开：高度自适应内容。用 grid-template-rows 的 0fr→1fr 技巧
   而不是 max-height —— 后者需要猜一个足够大的值，猜小了会截断，
   猜大了动画前段会有一大段空白。 */
.expand-enter-active,
.expand-leave-active {
  transition:
    opacity var(--duration-layout) var(--ease-expo),
    transform var(--duration-layout) var(--ease-expo);
}
.expand-enter-from,
.expand-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
