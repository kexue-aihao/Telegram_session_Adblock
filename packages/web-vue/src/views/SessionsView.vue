<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api, ApiError } from '@/lib/api';
import { CONTENT_TYPE_LABELS, relativeTime, SANCTION_LABELS, untilText } from '@/lib/format';
import type { RelayedMessage, Sanction, SessionDetail, SessionSummary } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useDebouncedRef } from '@/composables/useDebounced';
import { useChannel, useWsEvent } from '@/composables/useWsEvent';
import { useToastStore } from '@/stores/toast';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';
import SessionPane from '@/components/sessions/SessionPane.vue';

/**
 * 会话页：左列表 + 右聊天记录。
 *
 * 两者共享同一个路由（/sessions/:id），所以刷新页面、分享链接都能
 * 直接落到某个具体会话上。
 *
 * 一处值得注意的实现：**滚动容器用 flex-col-reverse**，
 * 因此视觉上的「底部」对应 scrollTop === 0。这样在上方插入更早的消息时
 * 浏览器会保持当前视口内容不动，不会产生滚动跳变 —— 这正是聊天界面
 * 一直用它的原因。
 */
const route = useRoute();
const router = useRouter();
const toast = useToastStore();

const query = ref('');
const debouncedQuery = useDebouncedRef(query, 300);
const statusFilter = ref<string>('');

const list = useAsync<{ items: SessionSummary[] }>(
  () =>
    api.get('/api/sessions', {
      q: debouncedQuery.value || undefined,
      status: statusFilter.value || undefined,
      limit: 50,
    }),
  () => [debouncedQuery.value, statusFilter.value],
);

useWsEvent('session.created', () => void list.reload());
useWsEvent<SessionSummary>('session.updated', (payload) => {
  const items = list.data.value?.items;
  if (!items) return;
  const index = items.findIndex((s) => s.id === payload.id);
  if (index >= 0) items[index] = payload;
});

const selectedId = computed(() => {
  const raw = route.params.id;
  const id = typeof raw === 'string' ? Number.parseInt(raw, 10) : NaN;
  return Number.isFinite(id) ? id : null;
});

const selected = computed(() =>
  list.data.value?.items.find((s) => s.id === selectedId.value) ?? null,
);

const stagger = (index: number) => ({
  animation: `row-in var(--duration-state) var(--ease-expo) ${Math.min(index, 10) * 25}ms both`,
});
</script>

<template>
  <div class="flex h-full min-h-0">
    <!-- ── 左：列表 ─────────────────────────────────────────── -->
    <div class="flex w-[320px] shrink-0 flex-col border-r border-[var(--color-line-faint)]">
      <div class="space-y-2 border-b border-[var(--color-line-faint)] p-3">
        <AppInput v-model="query" placeholder="搜索昵称、用户名或话题标题" class="h-8 text-xs" />
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            v-for="chip in [
              { label: '全部', value: '' },
              { label: '进行中', value: 'open' },
              { label: '已关闭', value: 'closed' },
            ]"
            :key="chip.value"
            type="button"
            class="rounded-full px-2.5 py-1 text-2xs transition-colors duration-[var(--duration-micro)]"
            :class="
              statusFilter === chip.value
                ? 'bg-[var(--color-accent-soft)] font-medium text-[var(--color-accent-bright)]'
                : 'text-[var(--color-ink-subtle)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink-muted)]'
            "
            @click="statusFilter = chip.value"
          >
            {{ chip.label }}
          </button>
        </div>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto">
        <div v-if="list.loading.value && !list.data.value" class="space-y-2 p-3">
          <AppSkeleton v-for="i in 5" :key="i" height="3.5rem" />
        </div>

        <AppEmpty
          v-else-if="(list.data.value?.items.length ?? 0) === 0"
          compact
          icon="sessions"
          :title="query ? '没有匹配的会话' : '还没有会话'"
          :description="query ? '换个关键词试试' : '当有用户私聊机器人时，这里会自动为每个人建立一个话题。'"
        />

        <button
          v-for="(item, index) in list.data.value?.items"
          :key="item.id"
          type="button"
          class="relative block w-full border-b border-[var(--color-line-faint)] px-4 py-3 text-left transition-colors duration-[var(--duration-micro)] last:border-b-0"
          :class="
            item.id === selectedId
              ? 'bg-[var(--color-active)]'
              : 'hover:bg-[var(--color-hover)]'
          "
          :style="stagger(index)"
          @click="router.push(`/sessions/${item.id}`)"
        >
          <!-- 选中标记：左侧一条竖线。比整行变色更克制，
               也不会与悬停态混淆 -->
          <span
            v-if="item.id === selectedId"
            class="absolute top-1/2 left-0 h-6 w-0.5 -translate-y-1/2 rounded-full bg-[var(--color-accent)]"
          />

          <div class="flex items-start gap-2.5">
            <div
              class="flex size-8 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-3)] text-xs font-medium text-[var(--color-ink-muted)]"
            >
              {{ (item.displayName[0] ?? '?').toUpperCase() }}
            </div>

            <div class="min-w-0 flex-1">
              <div class="flex items-baseline justify-between gap-2">
                <span class="truncate text-sm font-medium">{{ item.displayName }}</span>
                <span class="shrink-0 text-2xs text-[var(--color-ink-subtle)]">
                  {{ relativeTime(item.lastMessageAt ?? item.createdAt) }}
                </span>
              </div>

              <p class="mt-0.5 truncate text-xs text-[var(--color-ink-muted)]">
                <span v-if="item.lastMessageDirection === 'admin_to_user'" class="text-[var(--color-ink-subtle)]">
                  你：
                </span>
                {{ item.lastMessagePreview ?? '（暂无消息）' }}
              </p>

              <div class="mt-1 flex flex-wrap items-center gap-1.5">
                <AppBadge>{{ item.botName }}</AppBadge>
                <AppBadge v-if="item.violationScore > 0" tone="danger">违规 {{ item.violationScore }}</AppBadge>
                <AppBadge v-if="item.status === 'closed'">已关闭</AppBadge>
                <AppBadge v-if="item.isBlocked" tone="danger">已拉黑</AppBadge>
              </div>
            </div>
          </div>
        </button>
      </div>
    </div>

    <!-- ── 右：详情 ─────────────────────────────────────────── -->
    <div class="flex min-w-0 flex-1 flex-col">
      <Transition name="detail" mode="out-in">
        <AppEmpty
          v-if="!selectedId || !selected"
          key="empty"
          icon="sessions"
          title="选择一个会话"
          description="左侧列出了所有与你私聊过的用户。选中后可以查看完整往来并以管理员身份回复。"
        />

        <SessionPane v-else :key="selected.id" :session="selected" @deleted="router.push('/sessions')" />
      </Transition>
    </div>
  </div>
</template>



<style scoped>
@keyframes row-in {
  from {
    opacity: 0;
    transform: translateX(-4px);
  }
  to {
    opacity: 1;
    transform: translateX(0);
  }
}

.detail-enter-active,
.detail-leave-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.detail-enter-from,
.detail-leave-to {
  opacity: 0;
}
</style>
