<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue';
import { api, ApiError } from '@/lib/api';
import { CONTENT_TYPE_LABELS, relativeTime, SANCTION_LABELS, untilText } from '@/lib/format';
import type { RelayedMessage, SessionDetail, SessionSummary } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useChannel, useWsEvent } from '@/composables/useWsEvent';
import { useToastStore } from '@/stores/toast';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';

/**
 * 会话详情：用户信息 + 聊天记录 + 管理员回复框。
 *
 * 滚动用 flex-col-reverse：视觉上的「底部」对应 scrollTop === 0，
 * 因此在上方插入更早的消息时浏览器会保持视口内容不动。
 */
const props = defineProps<{ session: SessionSummary }>();
const emit = defineEmits<{ deleted: [] }>();

const toast = useToastStore();
const scrollRef = ref<HTMLDivElement | null>(null);
const draft = ref('');
const sending = ref(false);
const busy = ref(false);

const detail = useAsync<SessionDetail>(() => api.get(`/api/sessions/${props.session.id}`));

const messages = useAsync<{ items: RelayedMessage[]; nextCursor: number | null }>(() =>
  api.get(`/api/sessions/${props.session.id}/messages`, { limit: 50 }),
);

// 订阅这个话题的频道：只有订阅了才会收到它的新消息
useChannel(`topic:${props.session.id}`);

useWsEvent<RelayedMessage>('message.new', (payload) => {
  if (payload.topicId !== props.session.id) return;
  const item = messages.data.value;
  if (!item) return;
  // 列表是按 id 倒序存的，所以新消息插到最前面
  item.items = [payload, ...item.items];
  // 只在用户本来就贴着最新消息时才自动滚动。
  // 否则会把正在翻历史的人强行拽回底部 —— 聊天界面里最招人烦的行为。
  if (isAtLatest()) void scrollToLatest(true);
});

useWsEvent<RelayedMessage>('message.updated', (payload) => {
  if (payload.topicId !== props.session.id) return;
  const item = messages.data.value;
  if (!item) return;
  const index = item.items.findIndex((m) => m.id === payload.id);
  if (index >= 0) item.items[index] = payload;
});

useWsEvent<{ messageId: number; topicId: number }>('message.deleted', (payload) => {
  if (payload.topicId !== props.session.id) return;
  const item = messages.data.value;
  if (!item) return;
  const target = item.items.find((m) => m.id === payload.messageId);
  if (target) target.isDeleted = true;
});

/** 倒序存储 → 渲染前反转成「旧 → 新」的自然顺序 */
const ordered = computed(() => [...(messages.data.value?.items ?? [])].reverse());

function isAtLatest(): boolean {
  const el = scrollRef.value;
  if (!el) return true;
  return el.scrollTop < 120;
}

async function scrollToLatest(smooth: boolean) {
  await nextTick();
  scrollRef.value?.scrollTo({ top: 0, behavior: smooth ? 'smooth' : 'auto' });
}

onMounted(() => void scrollToLatest(false));

watch(
  () => props.session.id,
  () => void scrollToLatest(false),
);

async function onSend() {
  const text = draft.value.trim();
  if (!text || sending.value) return;

  sending.value = true;
  try {
    await api.post(`/api/sessions/${props.session.id}/messages`, { text });
    draft.value = '';
    void detail.reload();
  } catch (err) {
    toast.error('发送失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    sending.value = false;
  }
}

async function onAction(action: 'close' | 'reopen' | 'ban' | 'unban' | 'reset') {
  busy.value = true;
  try {
    if (action === 'close' || action === 'reopen') {
      await api.post(`/api/sessions/${props.session.id}/${action}`);
      toast.success(action === 'close' ? '会话已关闭' : '会话已重新打开');
    } else {
      const endpoint =
        action === 'reset'
          ? `/api/contacts/${props.session.contactId}/reset-violations`
          : `/api/contacts/${props.session.contactId}/${action}`;
      await api.post(endpoint);
      toast.success(
        action === 'ban' ? '已拉黑该用户' : action === 'unban' ? '已解除拉黑' : '违规分已清零',
      );
    }
    void detail.reload();
  } catch (err) {
    toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    busy.value = false;
  }
}

async function onDelete() {
  if (!confirm('这会在 Telegram 里真实删除整个话题及其中的全部消息。\n\n如果只是想停止接收该用户的消息，请改用「关闭」。\n\n确定删除？')) {
    return;
  }
  busy.value = true;
  try {
    await api.delete(`/api/sessions/${props.session.id}`);
    toast.success('会话已删除', 'Telegram 侧的话题也已一并删除');
    emit('deleted');
  } catch (err) {
    toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    busy.value = false;
  }
}

async function loadEarlier() {
  const cursor = messages.data.value?.nextCursor;
  if (!cursor) return;
  try {
    const next = await api.get<{ items: RelayedMessage[]; nextCursor: number | null }>(
      `/api/sessions/${props.session.id}/messages`,
      { cursor, limit: 50 },
    );
    const current = messages.data.value;
    if (!current) return;
    current.items = [...current.items, ...next.items];
    current.nextCursor = next.nextCursor;
  } catch (err) {
    toast.error('加载失败', err instanceof ApiError ? err.message : '未知错误');
  }
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- ── 头部 ─────────────────────────────────────────────── -->
    <header class="glass shrink-0 border-b border-[var(--color-line-faint)] px-4 py-3">
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h2 class="truncate text-base font-semibold tracking-tight">
              {{ session.displayName }}
            </h2>
            <code v-if="session.username" class="font-mono text-xs text-[var(--color-ink-subtle)]">
              @{{ session.username }}
            </code>
            <AppBadge v-if="session.isBlocked" tone="danger">已拉黑</AppBadge>
          </div>

          <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-ink-subtle)]">
            <span>TG ID <code class="font-mono text-[var(--color-ink-muted)]">{{ session.tgUserId }}</code></span>
            <span>话题 #{{ session.threadId }}</span>
            <span>{{ session.messageCount }} 条消息</span>
            <span>经由 {{ session.botName }}</span>
            <span v-if="session.violationScore > 0" class="text-[var(--color-danger)]">
              违规分 {{ session.violationScore }}
            </span>
          </div>
        </div>

        <div class="flex shrink-0 items-center gap-1.5">
          <AppButton
            v-if="session.status === 'open'"
            size="sm"
            variant="ghost"
            :disabled="busy"
            @click="onAction('close')"
          >
            关闭
          </AppButton>
          <AppButton v-else size="sm" variant="ghost" :disabled="busy" @click="onAction('reopen')">
            重开
          </AppButton>

          <AppButton
            v-if="session.isBlocked"
            size="sm"
            variant="ghost"
            :disabled="busy"
            @click="onAction('unban')"
          >
            解除拉黑
          </AppButton>
          <AppButton
            v-else
            size="sm"
            variant="ghost"
            class="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
            :disabled="busy"
            @click="onAction('ban')"
          >
            <AppIcon name="ban" :size="14" />
            拉黑
          </AppButton>

          <AppButton
            size="sm"
            variant="ghost"
            class="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
            aria-label="删除会话"
            :disabled="busy"
            @click="onDelete"
          >
            <AppIcon name="trash" :size="14" />
          </AppButton>
        </div>
      </div>

      <!-- 生效中的处罚横幅 -->
      <Transition name="banner">
        <div
          v-if="(detail.data.value?.activeSanctions.length ?? 0) > 0"
          class="mt-2.5 flex flex-wrap items-center gap-2"
        >
          <span
            v-for="s in detail.data.value?.activeSanctions"
            :key="s.id"
            class="flex items-center gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-1.5 text-2xs text-[var(--color-danger)]"
          >
            <AppIcon name="warning" :size="13" />
            {{ SANCTION_LABELS[s.type] }}
            <template v-if="s.expiresAt"> · {{ untilText(s.expiresAt) }}解除</template>
          </span>
          <AppButton size="sm" variant="ghost" :disabled="busy" @click="onAction('reset')">
            清零违规分
          </AppButton>
        </div>
      </Transition>
    </header>

    <!-- ── 消息流 ───────────────────────────────────────────── -->
    <div ref="scrollRef" class="flex min-h-0 flex-1 flex-col-reverse overflow-y-auto px-4 py-4">
      <div>
        <div v-if="messages.loading.value && !messages.data.value" class="space-y-3">
          <AppSkeleton v-for="i in 3" :key="i" height="3rem" width="65%" />
        </div>

        <AppEmpty
          v-else-if="(messages.data.value?.items.length ?? 0) === 0"
          compact
          icon="sessions"
          title="还没有往来消息"
          description="在下方输入框里可以直接以管理员身份给对方发消息。"
        />

        <div v-else class="space-y-2">
          <div
            v-for="message in ordered"
            :key="message.id"
            class="flex"
            :class="message.direction === 'admin_to_user' ? 'justify-end' : 'justify-start'"
          >
            <div
              class="max-w-[75%] space-y-1 rounded-2xl px-3.5 py-2.5 transition-opacity duration-[var(--duration-state)]"
              :class="[
                message.direction === 'admin_to_user'
                  ? 'bg-[color-mix(in_oklab,var(--color-accent)_16%,transparent)]'
                  : 'surface-2',
                message.isDeleted && 'opacity-45',
              ]"
            >
              <div class="flex items-center gap-2 text-2xs text-[var(--color-ink-subtle)]">
                <span>{{ message.direction === 'admin_to_user' ? (message.senderLabel ?? '管理员') : '用户' }}</span>
                <span>·</span>
                <span>{{ relativeTime(message.createdAt) }}</span>
                <span v-if="message.editedAt" class="italic">已编辑</span>
                <span v-if="message.isDeleted" class="text-[var(--color-danger)]">已删除</span>
              </div>

              <p
                v-if="message.content.text ?? message.content.caption"
                class="text-sm leading-relaxed break-words whitespace-pre-wrap"
              >
                {{ message.content.text ?? message.content.caption }}
              </p>

              <!-- 非文本内容用一条摘要表示；媒体不内联播放，
                   避免面板加载大量文件 -->
              <div
                v-if="message.content.type !== 'text'"
                class="flex items-center gap-1.5 text-xs text-[var(--color-ink-muted)]"
              >
                <span class="rounded-md bg-[var(--color-active)] px-1.5 py-0.5">
                  {{ CONTENT_TYPE_LABELS[message.content.type] ?? message.content.type }}
                </span>
                <span v-if="message.content.media.length > 1">×{{ message.content.media.length }}</span>
              </div>

              <p
                v-if="message.content.hasHiddenLink"
                class="flex items-center gap-1 text-2xs text-[var(--color-warn)]"
              >
                <AppIcon name="warning" :size="12" />
                含隐藏链接
              </p>
            </div>
          </div>

          <div v-if="messages.data.value?.nextCursor" class="flex justify-center py-2">
            <AppButton size="sm" variant="ghost" @click="loadEarlier">加载更早的消息</AppButton>
          </div>
        </div>
      </div>
    </div>

    <!-- ── 输入区 ───────────────────────────────────────────── -->
    <div class="shrink-0 border-t border-[var(--color-line-faint)] p-3">
      <p
        v-if="session.isBlocked"
        class="rounded-xl bg-[var(--color-danger-soft)] px-3 py-2.5 text-xs text-[var(--color-danger)]"
      >
        该用户已被拉黑，解除后才能发送消息
      </p>

      <div v-else class="flex items-end gap-2">
        <textarea
          v-model="draft"
          rows="1"
          placeholder="以管理员身份回复…（Enter 发送，Shift+Enter 换行）"
          class="max-h-40 min-h-9 flex-1 resize-none rounded-xl border border-[var(--color-line)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-ink)] transition-colors duration-[var(--duration-micro)] placeholder:text-[var(--color-ink-faint)] focus:border-[var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)] focus:outline-none"
          @keydown.enter.exact.prevent="onSend"
        />
        <AppButton
          variant="primary"
          :loading="sending"
          :disabled="!draft.trim()"
          aria-label="发送"
          @click="onSend"
        >
          <AppIcon name="send" :size="16" />
        </AppButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.banner-enter-active {
  transition: all var(--duration-layout) var(--ease-expo);
}
.banner-enter-from {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
