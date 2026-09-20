<script setup lang="ts">
import { ref } from 'vue';
import { api, ApiError } from '@/lib/api';
import { relativeTime, HEALTH_LABELS } from '@/lib/format';
import type { Bot, GroupCheck } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useWsEvent } from '@/composables/useWsEvent';
import { useToastStore } from '@/stores/toast';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppCard from '@/components/ui/AppCard.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppModal from '@/components/ui/AppModal.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';
import AppSwitch from '@/components/ui/AppSwitch.vue';
import BotWizard from '@/components/bots/BotWizard.vue';

/**
 * 机器人管理。
 *
 * 创建流程复刻 @BotFather 的三步（申请 token → 校验 → 绑定管理群），
 * 但把每一步的**失败原因**直接摆在界面上。这个产品的搭建门槛几乎全在这里，
 * 而失败原因几乎全是 Telegram 侧的权限与配置问题 —— 一句「校验失败」
 * 会让用户去猜半小时。
 */
const toast = useToastStore();
const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'));

useWsEvent('bot.status', () => void bots.reload());

const wizardOpen = ref(false);
const deleteTarget = ref<Bot | null>(null);
const deleting = ref(false);
const checkingId = ref<number | null>(null);
const groupChecks = ref<Record<number, GroupCheck>>({});

const stagger = (index: number) => ({
  animation: `card-in var(--duration-layout) var(--ease-expo) ${Math.min(index, 8) * 40}ms both`,
});

async function toggleEnabled(bot: Bot, next: boolean) {
  const previous = bot.isEnabled;
  bot.isEnabled = next;
  try {
    await api.patch(`/api/bots/${bot.id}`, { isEnabled: next });
    toast.success(next ? '已启用机器人' : '已停用机器人');
  } catch (err) {
    bot.isEnabled = previous;
    toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
  }
}

async function checkGroup(bot: Bot) {
  checkingId.value = bot.id;
  try {
    const result = await api.post<GroupCheck>(`/api/bots/${bot.id}/check-group`);
    groupChecks.value = { ...groupChecks.value, [bot.id]: result };
  } catch (err) {
    toast.error('体检失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    checkingId.value = null;
  }
}

async function reload(bot: Bot) {
  checkingId.value = bot.id;
  try {
    await api.post(`/api/bots/${bot.id}/reload`);
    toast.success('已重新加载');
    void bots.reload();
  } catch (err) {
    toast.error('重新加载失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    checkingId.value = null;
  }
}

/**
 * 切换「管理机器人」。
 *
 * 开启后这个机器人会接受管理命令：管理员私聊它，发 /start 就有菜单，
 * 可以直接在 Telegram 里增删托管其他机器人。
 *
 * 前提是「设置」页里填了管理员的 Telegram 用户 ID —— 没填的话
 * 所有管理命令都会被拒绝（这是一条安全边界，不是可选的便利项）。
 */
async function toggleManager(bot: Bot) {
  const next = !bot.isManager;
  checkingId.value = bot.id;
  try {
    await api.patch(`/api/bots/${bot.id}`, { isManager: next });
    if (next) {
      toast.success('已设为管理机器人', '在 Telegram 里私聊它并发送 /start');
    } else {
      toast.success('已取消管理机器人');
    }
    void bots.reload();
  } catch (err) {
    toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    checkingId.value = null;
  }
}

async function confirmDelete() {
  const target = deleteTarget.value;
  if (!target) return;

  deleting.value = true;
  try {
    await api.delete(`/api/bots/${target.id}`);
    toast.success('机器人已删除', `@${target.username} 及其会话记录已一并清除`);
    deleteTarget.value = null;
    void bots.reload();
  } catch (err) {
    toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    deleting.value = false;
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[1440px] space-y-5 p-5 lg:p-6">
    <div class="flex items-start justify-between gap-4">
      <div class="space-y-0.5">
        <h2 class="text-lg font-semibold tracking-tight">机器人</h2>
        <p class="max-w-2xl text-xs leading-relaxed text-[var(--color-ink-muted)]">
          每个机器人独立轮询、独立话题空间；token 以 AES-256-GCM 加密存储，界面上永不回显明文。
        </p>
      </div>
      <AppButton variant="primary" @click="wizardOpen = true">
        <AppIcon name="plus" :size="16" />
        创建机器人
      </AppButton>
    </div>

    <div
      v-if="bots.loading.value && !bots.data.value"
      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3"
    >
      <AppSkeleton v-for="i in 3" :key="i" height="11rem" radius="var(--radius-2xl)" />
    </div>

    <AppCard v-else-if="(bots.data.value?.items.length ?? 0) === 0" flush>
      <AppEmpty
        icon="bot"
        title="还没有添加机器人"
        description="先在 @BotFather 那里申请一个机器人拿到 token，再回到这里粘贴。整个过程大约两分钟。"
      >
        <AppButton variant="primary" @click="wizardOpen = true">
          <AppIcon name="plus" :size="16" />
          开始创建
        </AppButton>
      </AppEmpty>
    </AppCard>

    <div v-else class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      <AppCard
        v-for="(bot, index) in bots.data.value?.items"
        :key="bot.id"
        :style="stagger(index)"
        class="flex flex-col"
      >
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="relative inline-flex size-2 shrink-0">
                <span
                  v-if="bot.healthStatus === 'starting'"
                  class="pulse-ring absolute inset-0 rounded-full bg-[var(--color-warn)]"
                />
                <span
                  class="relative inline-flex size-2 rounded-full"
                  :class="{
                    'bg-[var(--color-success)]': bot.healthStatus === 'online',
                    'bg-[var(--color-warn)]': bot.healthStatus === 'starting',
                    'bg-[var(--color-danger)]': bot.healthStatus === 'error',
                    'bg-[var(--color-ink-subtle)]':
                      bot.healthStatus === 'stopped' || bot.healthStatus === 'unknown',
                  }"
                />
              </span>
              <h3 class="truncate text-sm font-medium">{{ bot.name }}</h3>
            </div>
            <code class="mt-0.5 block truncate font-mono text-xs text-[var(--color-ink-subtle)]">
              @{{ bot.username }}
            </code>
          </div>

          <AppSwitch
            :model-value="bot.isEnabled"
            label="启用机器人"
            @update:model-value="(v) => toggleEnabled(bot, v)"
          />
        </div>

        <dl class="mt-3.5 space-y-1.5 text-xs">
          <div class="flex items-center justify-between gap-3">
            <dt class="text-[var(--color-ink-subtle)]">状态</dt>
            <dd>
              <AppBadge
                :tone="
                  bot.healthStatus === 'online'
                    ? 'success'
                    : bot.healthStatus === 'error'
                      ? 'danger'
                      : bot.healthStatus === 'starting'
                        ? 'warn'
                        : 'neutral'
                "
              >
                {{ HEALTH_LABELS[bot.healthStatus] }}
              </AppBadge>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-[var(--color-ink-subtle)]">管理群</dt>
            <dd class="min-w-0 truncate text-[var(--color-ink-muted)]">
              {{ bot.adminGroupTitle ?? (bot.adminGroupId ? bot.adminGroupId : '未绑定') }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-[var(--color-ink-subtle)]">Token</dt>
            <dd>
              <code class="font-mono text-[var(--color-ink-muted)]">{{ bot.tokenMask }}</code>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-[var(--color-ink-subtle)]">最后轮询</dt>
            <dd class="text-[var(--color-ink-muted)]">{{ relativeTime(bot.lastPolledAt) }}</dd>
          </div>
        </dl>

        <p
          v-if="bot.lastError"
          class="mt-2.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]"
        >
          {{ bot.lastError }}
        </p>

        <!--
          中继失败的原因。
          这是「用户发了消息但会话列表里什么都没有」的唯一面板线索 ——
          在这之前它只写进容器日志，而很多人根本不知道要去看那里。
        -->
        <div
          v-if="bot.lastRelayError"
          class="mt-2.5 space-y-1 rounded-lg bg-[var(--color-warn-soft)] px-2.5 py-2"
        >
          <p class="flex items-center gap-1.5 text-2xs font-medium text-[var(--color-warn)]">
            <AppIcon name="warning" :size="14" class="shrink-0" />
            消息未能中继
            <span class="font-normal text-[var(--color-ink-subtle)]">
              · {{ relativeTime(bot.lastRelayErrorAt) }}
            </span>
          </p>
          <p class="text-2xs leading-relaxed whitespace-pre-wrap text-[var(--color-ink-muted)]">
            {{ bot.lastRelayError }}
          </p>
        </div>

        <p
          v-if="!bot.adminGroupId"
          class="mt-2.5 flex items-start gap-1.5 rounded-lg bg-[var(--color-warn-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-warn)]"
        >
          <AppIcon name="warning" :size="14" class="mt-px shrink-0" />
          尚未绑定管理群，用户私聊的消息无法中继进去。
        </p>

        <div class="mt-3.5 flex items-center gap-1.5 border-t border-[var(--color-line-faint)] pt-3">
          <AppButton size="sm" variant="ghost" :loading="checkingId === bot.id" @click="checkGroup(bot)">
            <AppIcon name="check" :size="14" />
            群体检
          </AppButton>
          <AppButton size="sm" variant="ghost" :disabled="checkingId === bot.id" @click="reload(bot)">
            <AppIcon name="refresh" :size="14" />
            重载
          </AppButton>
          <!--
            管理机器人开关。开启后可以直接在 Telegram 里私聊这个机器人，
            用 /start 打开菜单来托管其他机器人 —— 不必为了加一个机器人回面板。
          -->
          <AppButton
            size="sm"
            variant="ghost"
            :class="bot.isManager ? 'text-[var(--color-accent-bright)]' : ''"
            :disabled="checkingId === bot.id"
            @click="toggleManager(bot)"
          >
            <AppIcon name="command" :size="14" />
            {{ bot.isManager ? '管理机器人' : '设为管理' }}
          </AppButton>
          <AppButton
            size="sm"
            variant="ghost"
            class="ml-auto text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
            aria-label="删除机器人"
            @click="deleteTarget = bot"
          >
            <AppIcon name="trash" :size="14" />
          </AppButton>
        </div>

        <Transition name="err">
          <div
            v-if="groupChecks[bot.id]"
            class="mt-3 space-y-1.5 rounded-lg border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] px-2.5 py-2"
          >
            <p class="text-2xs font-medium">
              {{ groupChecks[bot.id]?.ok ? '✅ 管理群配置正常' : '⚠️ 管理群配置有问题' }}
            </p>
            <p
              v-for="problem in groupChecks[bot.id]?.problems"
              :key="problem"
              class="text-2xs leading-relaxed text-[var(--color-ink-muted)]"
            >
              · {{ problem }}
            </p>
            <button
              type="button"
              class="text-2xs text-[var(--color-ink-subtle)] hover:underline"
              @click="delete groupChecks[bot.id]"
            >
              收起
            </button>
          </div>
        </Transition>
      </AppCard>
    </div>

    <BotWizard
      :open="wizardOpen"
      @close="wizardOpen = false"
      @created="
        () => {
          wizardOpen = false;
          bots.reload();
        }
      "
    />

    <AppModal
      :open="deleteTarget !== null"
      title="删除机器人"
      width="26rem"
      @close="deleteTarget = null"
    >
      <p class="text-sm leading-relaxed text-[var(--color-ink-muted)]">
        将删除
        <code class="font-mono text-[var(--color-ink)]">{{ deleteTarget?.name }}</code>
        及其<strong class="text-[var(--color-ink)]">全部会话、消息与命中记录</strong>。此操作不可撤销。
        <br /><br />
        Telegram 侧的群与话题不会被删除，机器人只是不再响应。
      </p>
      <template #footer>
        <AppButton variant="ghost" :disabled="deleting" @click="deleteTarget = null">取消</AppButton>
        <AppButton variant="danger" :loading="deleting" @click="confirmDelete">确认删除</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
@keyframes card-in {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
.err-enter-active {
  transition: all var(--duration-state) var(--ease-expo);
}
.err-enter-from {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
