<script setup lang="ts">
import { computed, ref } from 'vue';
import { api, ApiError } from '@/lib/api';
import { relativeTime, HEALTH_LABELS } from '@/lib/format';
import type { Bot, GlobalSettings, GroupCheck } from '@/lib/types';
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
 * 页面上是两类**完全不同**的东西，因此分成两区：
 *
 *   管理机器人 —— 一台，你在 Telegram 里的操作入口，**不参与转发**
 *   托管机器人 —— 若干台，各自接管一批用户的会话转发
 *
 * 混在一起显示会让人以为它们是一回事，而「为什么这台机器人不转发消息」
 * 或者反过来「为什么陌生人的消息进了我的控制台」都会变成要排查的问题。
 *
 * 创建流程复刻 @BotFather 的步骤，但把每一步的**失败原因**直接摆在界面上。
 * 这个产品的搭建门槛几乎全在这里，而失败原因几乎全是 Telegram 侧的权限
 * 与配置问题 —— 一句「校验失败」会让用户去猜半小时。
 */
const toast = useToastStore();
const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'));
const settings = useAsync<GlobalSettings>(() => api.get('/api/settings'));

useWsEvent('bot.status', () => void bots.reload());

const wizardOpen = ref(false);
const wizardMode = ref<'relay' | 'manager'>('relay');
const deleteTarget = ref<Bot | null>(null);
const deleting = ref(false);
const checkingId = ref<number | null>(null);
const groupChecks = ref<Record<number, GroupCheck>>({});

/** 待提升为控制台的机器人（要弹确认，见 promoteToConsole） */
const promoteTarget = ref<Bot | null>(null);
const promoting = ref(false);

const allBots = computed(() => bots.data.value?.items ?? []);
/** 控制台至多一台，后端保证唯一 */
const consoleBot = computed(() => allBots.value.find((b) => b.isManager) ?? null);
/** 托管（转发）机器人 */
const hostedBots = computed(() => allBots.value.filter((b) => !b.isManager));

/** 没有配置管理员 Telegram ID 时，控制台的命令一律被拒绝 */
const adminConfigured = computed(() => (settings.data.value?.adminTgUserId ?? 0) !== 0);

const stagger = (index: number) => ({
  animation: `card-in var(--duration-layout) var(--ease-expo) ${Math.min(index, 8) * 40}ms both`,
});

function openWizard(mode: 'relay' | 'manager') {
  wizardMode.value = mode;
  wizardOpen.value = true;
}

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
 * 把一台已有的机器人提升为控制台。
 *
 * 要确认再执行：这不是一个「加个标记」的操作 —— 控制台不参与转发，
 * 提升之后它手上的会话就转发了，而当前那台控制台会被解绑、
 * 变回一台没有绑定管理群的转发机器人。
 */
async function promoteToConsole() {
  const target = promoteTarget.value;
  if (!target) return;

  promoting.value = true;
  try {
    await api.patch(`/api/bots/${target.id}`, { isManager: true });
    toast.success(
      '已设为管理机器人',
      `在 Telegram 里私聊 @${target.username} 发 /start`,
    );
    promoteTarget.value = null;
    void bots.reload();
  } catch (err) {
    toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    promoting.value = false;
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
  <div class="mx-auto w-full max-w-[1440px] space-y-7 p-5 lg:p-6">
    <div class="flex items-start justify-between gap-4">
      <div class="space-y-0.5">
        <h2 class="text-lg font-semibold tracking-tight">机器人</h2>
        <p class="max-w-2xl text-xs leading-relaxed text-[var(--color-ink-muted)]">
          每个机器人独立轮询、独立话题空间；token 以 AES-256-GCM 加密存储，界面上永不回显明文。
        </p>
      </div>
      <AppButton variant="primary" @click="openWizard('relay')">
        <AppIcon name="plus" :size="16" />
        添加托管机器人
      </AppButton>
    </div>

    <div
      v-if="bots.loading.value && !bots.data.value"
      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3"
    >
      <AppSkeleton v-for="i in 3" :key="i" height="11rem" radius="var(--radius-2xl)" />
    </div>

    <template v-else>
      <!-- ────────────── 管理机器人（控制台） ────────────── -->
      <section class="space-y-3">
        <div class="flex items-end justify-between gap-4">
          <div class="space-y-0.5">
            <h3 class="text-sm font-medium">管理机器人</h3>
            <p class="max-w-2xl text-2xs leading-relaxed text-[var(--color-ink-muted)]">
              你在 Telegram 里的操作入口：私聊它发 <code class="font-mono">/start</code>
              就能增删托管其他机器人。它<strong>不参与转发</strong> ——
              谁给它发消息都不会变成话题，因此它与下面的托管机器人各用各的 token。
            </p>
          </div>
          <AppButton
            v-if="consoleBot"
            size="sm"
            variant="ghost"
            class="shrink-0"
            @click="openWizard('manager')"
          >
            <AppIcon name="command" :size="14" />
            更换
          </AppButton>
        </div>

        <AppCard v-if="consoleBot" class="flex flex-col">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="relative inline-flex size-2 shrink-0">
                  <span
                    v-if="consoleBot.healthStatus === 'starting'"
                    class="pulse-ring absolute inset-0 rounded-full bg-[var(--color-warn)]"
                  />
                  <span
                    class="relative inline-flex size-2 rounded-full"
                    :class="{
                      'bg-[var(--color-success)]': consoleBot.healthStatus === 'online',
                      'bg-[var(--color-warn)]': consoleBot.healthStatus === 'starting',
                      'bg-[var(--color-danger)]': consoleBot.healthStatus === 'error',
                      'bg-[var(--color-ink-subtle)]':
                        consoleBot.healthStatus === 'stopped' ||
                        consoleBot.healthStatus === 'unknown',
                    }"
                  />
                </span>
                <h3 class="truncate text-sm font-medium">{{ consoleBot.name }}</h3>
                <AppBadge tone="accent">控制台</AppBadge>
              </div>
              <code class="mt-0.5 block truncate font-mono text-xs text-[var(--color-ink-subtle)]">
                @{{ consoleBot.username }}
              </code>
            </div>

            <AppSwitch
              :model-value="consoleBot.isEnabled"
              label="启用机器人"
              @update:model-value="(v) => toggleEnabled(consoleBot!, v)"
            />
          </div>

          <dl class="mt-3.5 space-y-1.5 text-xs">
            <div class="flex items-center justify-between gap-3">
              <dt class="text-[var(--color-ink-subtle)]">状态</dt>
              <dd>
                <AppBadge
                  :tone="
                    consoleBot.healthStatus === 'online'
                      ? 'success'
                      : consoleBot.healthStatus === 'error'
                        ? 'danger'
                        : consoleBot.healthStatus === 'starting'
                          ? 'warn'
                          : 'neutral'
                  "
                >
                  {{ HEALTH_LABELS[consoleBot.healthStatus] }}
                </AppBadge>
              </dd>
            </div>
            <div class="flex items-center justify-between gap-3">
              <dt class="text-[var(--color-ink-subtle)]">Token</dt>
              <dd>
                <code class="font-mono text-[var(--color-ink-muted)]">
                  {{ consoleBot.tokenMask }}
                </code>
              </dd>
            </div>
            <div class="flex items-center justify-between gap-3">
              <dt class="text-[var(--color-ink-subtle)]">最后轮询</dt>
              <dd class="text-[var(--color-ink-muted)]">
                {{ relativeTime(consoleBot.lastPolledAt) }}
              </dd>
            </div>
          </dl>

          <p
            v-if="consoleBot.lastError"
            class="mt-2.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]"
          >
            {{ consoleBot.lastError }}
          </p>

          <!--
            没填管理员 Telegram ID 时，控制台会把所有命令都拒掉 ——
            这是一条安全边界，但现象是「发什么都没反应」，
            不指出来就会被当成故障。
          -->
          <p
            v-if="!adminConfigured"
            class="mt-2.5 flex items-start gap-1.5 rounded-lg bg-[var(--color-warn-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-warn)]"
          >
            <AppIcon name="warning" :size="14" class="mt-px shrink-0" />
            <span>
              还没有在「设置」里填管理员 Telegram 用户 ID，因此
              <strong>所有管理命令都会被拒绝</strong>。填上你自己的 ID 之后，
              私聊它发 <code class="font-mono">/start</code> 才有反应。
            </span>
          </p>

          <div class="mt-3.5 flex items-center gap-1.5 border-t border-[var(--color-line-faint)] pt-3">
            <AppButton
              size="sm"
              variant="ghost"
              :disabled="checkingId === consoleBot.id"
              @click="reload(consoleBot)"
            >
              <AppIcon name="refresh" :size="14" />
              重载
            </AppButton>
            <AppButton
              size="sm"
              variant="ghost"
              class="ml-auto text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
              aria-label="删除管理机器人"
              @click="deleteTarget = consoleBot"
            >
              <AppIcon name="trash" :size="14" />
            </AppButton>
          </div>
        </AppCard>

        <AppCard v-else flush>
          <AppEmpty
            icon="command"
            title="还没有绑定管理机器人"
            description="绑定之后可以直接在 Telegram 里增删托管其他机器人，不必回面板。它需要单独在 @BotFather 申请，且不参与转发。"
          >
            <AppButton variant="primary" @click="openWizard('manager')">
              <AppIcon name="plus" :size="16" />
              绑定管理机器人
            </AppButton>
          </AppEmpty>
        </AppCard>
      </section>

      <!-- ────────────── 托管机器人（转发） ────────────── -->
      <section class="space-y-3">
        <div class="space-y-0.5">
          <h3 class="text-sm font-medium">托管机器人</h3>
          <p class="max-w-2xl text-2xs leading-relaxed text-[var(--color-ink-muted)]">
            用户私聊它们，消息进入各自绑定的管理群话题。
          </p>
        </div>

        <AppCard v-if="hostedBots.length === 0" flush>
          <AppEmpty
            icon="bot"
            title="还没有托管机器人"
            description="先在 @BotFather 那里申请一个机器人拿到 token，再回到这里粘贴。整个过程大约两分钟。"
          >
            <AppButton variant="primary" @click="openWizard('relay')">
              <AppIcon name="plus" :size="16" />
              添加托管机器人
            </AppButton>
          </AppEmpty>
        </AppCard>

        <div v-else class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          <AppCard
            v-for="(bot, index) in hostedBots"
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
              把已有的一台提升为控制台。
              要确认再执行：控制台不参与转发，提升之后它手上的会话就转发了，
              而当前那台控制台会被解绑、变回一台没有管理群的转发机器人。
            -->
            <AppButton
              size="sm"
              variant="ghost"
              :disabled="checkingId === bot.id"
              @click="promoteTarget = bot"
            >
              <AppIcon name="command" :size="14" />
              设为控制台
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
      </section>
    </template>

    <BotWizard
      :open="wizardOpen"
      :mode="wizardMode"
      @close="wizardOpen = false"
      @created="
        () => {
          wizardOpen = false;
          bots.reload();
          settings.reload();
        }
      "
    />

    <!-- 提升为控制台的确认 -->
    <AppModal
      :open="promoteTarget !== null"
      title="设为管理机器人"
      width="26rem"
      @close="promoteTarget = null"
    >
      <p class="text-sm leading-relaxed text-[var(--color-ink-muted)]">
        将把
        <code class="font-mono text-[var(--color-ink)]">@{{ promoteTarget?.username }}</code>
        设为控制台。它<strong class="text-[var(--color-ink)]">不再转发任何消息</strong> ——
        用户私聊它不会变成话题，已有的会话也不再更新。
        <template v-if="consoleBot">
          <br /><br />
          当前的控制台
          <code class="font-mono text-[var(--color-ink)]">@{{ consoleBot.username }}</code>
          会被解绑，变回一台没有绑定管理群的普通机器人。
        </template>
      </p>
      <template #footer>
        <AppButton variant="ghost" :disabled="promoting" @click="promoteTarget = null">
          取消
        </AppButton>
        <AppButton variant="primary" :loading="promoting" @click="promoteToConsole">
          确认设置
        </AppButton>
      </template>
    </AppModal>

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
