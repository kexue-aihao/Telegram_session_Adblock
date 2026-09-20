<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { api, ApiError } from '@/lib/api';
import type { Bot, BotSettings, GlobalSettings } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useToastStore } from '@/stores/toast';
import { useAuthStore } from '@/stores/auth';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppCard from '@/components/ui/AppCard.vue';
import AppField from '@/components/ui/AppField.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppSelect from '@/components/ui/AppSelect.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';
import AppSwitch from '@/components/ui/AppSwitch.vue';
import AppTextarea from '@/components/ui/AppTextarea.vue';
import EscalationEditor from '@/components/settings/EscalationEditor.vue';

/**
 * 设置页。
 *
 * 分三段：全局、单机器人、账号。单机器人那一段是真正复杂的地方 ——
 * 阶梯处罚与文案模板都在那里，而且它们直接决定用户会不会被误伤，
 * 所以每一档都给出「这一档会发生什么」的人话说明，而不只是放几个数字输入框。
 */
const toast = useToastStore();
const auth = useAuthStore();

const global = useAsync<GlobalSettings>(() => api.get('/api/settings'));
const globalDraft = ref<GlobalSettings | null>(null);
const savingGlobal = ref(false);

watch(
  () => global.data.value,
  (v) => {
    if (v) globalDraft.value = { ...v };
  },
  { immediate: true },
);

const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'));
const selectedBotId = ref<number | null>(null);

watch(
  () => bots.data.value?.items,
  (items) => {
    if (selectedBotId.value === null && items && items.length > 0) {
      selectedBotId.value = items[0]!.id;
    }
  },
  { immediate: true },
);

const botSettings = useAsync<BotSettings>(
  () => api.get(`/api/bots/${selectedBotId.value}/settings`),
  () => [selectedBotId.value],
);

const botDraft = ref<BotSettings | null>(null);
const savingBot = ref(false);

watch(
  () => botSettings.data.value,
  (v) => {
    if (v) botDraft.value = structuredClone(v);
  },
  { immediate: true },
);

async function saveGlobal() {
  if (!globalDraft.value) return;
  savingGlobal.value = true;
  try {
    await api.patch('/api/settings', globalDraft.value);
    toast.success('设置已保存');
  } catch (err) {
    toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    savingGlobal.value = false;
  }
}

async function saveBot() {
  if (!botDraft.value || selectedBotId.value === null) return;
  savingBot.value = true;
  try {
    const { botId: _ignored, ...payload } = botDraft.value;
    void _ignored;
    await api.patch(`/api/bots/${selectedBotId.value}/settings`, payload);
    toast.success('机器人设置已保存', '立即生效，无需重载');
  } catch (err) {
    toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    savingBot.value = false;
  }
}

// ── 账号 ──────────────────────────────────────────────────────
const currentPassword = ref('');
const newPassword = ref('');
const confirmPassword = ref('');
const savingPassword = ref(false);

const passwordMismatch = computed(
  () => confirmPassword.value.length > 0 && newPassword.value !== confirmPassword.value,
);
const passwordTooWeak = computed(
  () => newPassword.value.length > 0 && !(/[a-zA-Z]/.test(newPassword.value) && /[0-9]/.test(newPassword.value)),
);
const canSubmitPassword = computed(
  () =>
    currentPassword.value.length > 0 &&
    newPassword.value.length >= 8 &&
    !passwordMismatch.value &&
    !passwordTooWeak.value,
);

async function changePassword() {
  savingPassword.value = true;
  try {
    const result = await api.post<{ ok: boolean; revokedSessions: number }>(
      '/api/auth/password',
      { currentPassword: currentPassword.value, newPassword: newPassword.value },
    );
    toast.success(
      '密码已更新',
      result.revokedSessions > 0
        ? `同时登录的其它 ${result.revokedSessions} 个会话已被登出`
        : '当前会话保持登录状态',
    );
    currentPassword.value = '';
    newPassword.value = '';
    confirmPassword.value = '';
  } catch (err) {
    toast.error('修改失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    savingPassword.value = false;
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[1100px] space-y-5 p-5 lg:p-6">
    <div class="space-y-0.5">
      <h2 class="text-lg font-semibold tracking-tight">设置</h2>
      <p class="text-xs text-[var(--color-ink-muted)]">
        全局配置、单机器人行为与账号安全。改动即时生效，不需要重启服务。
      </p>
    </div>

    <!-- ── 全局 ─────────────────────────────────────────────── -->
    <AppCard>
      <h3 class="text-base font-semibold tracking-tight">全局</h3>
      <p class="mt-0.5 text-xs text-[var(--color-ink-muted)]">对所有机器人生效</p>

      <div v-if="!globalDraft" class="mt-4 space-y-2">
        <AppSkeleton height="2.25rem" />
        <AppSkeleton height="2.25rem" />
      </div>

      <div v-else class="mt-4 space-y-4">
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <AppField label="面板时区" hint="影响统计按哪一天切分">
            <AppSelect v-model="globalDraft.timezone">
              <option value="Asia/Shanghai">Asia/Shanghai（北京时间）</option>
              <option value="Asia/Tokyo">Asia/Tokyo</option>
              <option value="Asia/Singapore">Asia/Singapore</option>
              <option value="Europe/London">Europe/London</option>
              <option value="America/New_York">America/New_York</option>
              <option value="UTC">UTC</option>
            </AppSelect>
          </AppField>

          <AppField label="命中审计保留天数" hint="留空表示永久保留">
            <AppInput
              :model-value="globalDraft.auditRetentionDays ?? ''"
              type="number"
              min="1"
              max="3650"
              placeholder="永久保留"
              @update:model-value="
                globalDraft.auditRetentionDays = $event ? Number.parseInt($event, 10) : null
              "
            />
          </AppField>
        </div>

        <div
          class="flex items-start justify-between gap-3 rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] px-3.5 py-3"
        >
          <div class="min-w-0">
            <p class="text-xs font-medium">命中后自动停用异常的规则</p>
            <p class="mt-0.5 text-2xs leading-relaxed text-[var(--color-ink-subtle)]">
              强烈建议开启。一条行为异常的规则会拖慢所有机器人的消息处理。
            </p>
          </div>
          <AppSwitch v-model="globalDraft.autoDisableOnRegexTimeout" label="自动停用异常规则" />
        </div>

        <div class="flex justify-end">
          <AppButton variant="primary" :loading="savingGlobal" @click="saveGlobal">
            保存全局设置
          </AppButton>
        </div>
      </div>
    </AppCard>

    <!-- ── 单机器人 ─────────────────────────────────────────── -->
    <AppCard>
      <div class="flex items-start justify-between gap-4">
        <div>
          <h3 class="text-base font-semibold tracking-tight">机器人设置</h3>
          <p class="mt-0.5 text-xs text-[var(--color-ink-muted)]">
            阶梯处罚、话题行为与全部面向用户的文案
          </p>
        </div>
        <AppSelect
          v-if="(bots.data.value?.items.length ?? 0) > 0"
          v-model="selectedBotId"
          class="h-8 w-48 text-xs"
        >
          <option v-for="bot in bots.data.value?.items" :key="bot.id" :value="bot.id">
            {{ bot.name }}
          </option>
        </AppSelect>
      </div>

      <p v-if="(bots.data.value?.items.length ?? 0) === 0" class="mt-4 text-xs text-[var(--color-ink-subtle)]">
        还没有添加机器人。
      </p>

      <div v-else-if="!botDraft" class="mt-4 space-y-2">
        <AppSkeleton height="6rem" />
        <AppSkeleton height="6rem" />
      </div>

      <div v-else class="mt-4 space-y-6">
        <!-- 阶梯处罚 -->
        <section class="space-y-3">
          <div>
            <h4 class="text-sm font-medium">阶梯处罚</h4>
            <p class="mt-0.5 text-2xs leading-relaxed text-[var(--color-ink-subtle)]">
              违规分由规则命中累加（取单次命中的最高分，不叠加）。达到某一档的分数时自动升级处置
              —— 这是「用户不在管理群里、Telegram 原生禁言用不了」这个约束下的替代方案。
            </p>
          </div>

          <EscalationEditor v-model="botDraft.escalation" />

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <AppField label="违规分衰减周期" hint="多少天无违规后减半；留空表示永不衰减">
              <AppInput
                :model-value="botDraft.escalation.decayDays ?? ''"
                type="number"
                min="1"
                max="3650"
                placeholder="永不衰减"
                @update:model-value="
                  botDraft.escalation.decayDays = $event ? Number.parseInt($event, 10) : null
                "
              />
            </AppField>

            <AppField label="违规分上限" hint="防止无限累加">
              <AppInput v-model.number="botDraft.escalation.maxScore" type="number" min="1" max="100000" />
            </AppField>
          </div>
        </section>

        <!-- 话题与中继 -->
        <section class="space-y-3 border-t border-[var(--color-line-faint)] pt-5">
          <h4 class="text-sm font-medium">话题与中继</h4>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <AppField label="话题命名模板" hint="{name} {username} {id} {botName}">
              <AppInput v-model="botDraft.topicNameTemplate" class="font-mono text-xs" />
            </AppField>

            <AppField label="自动归档" hint="话题静默多少小时后自动关闭；留空表示不自动关闭">
              <AppInput
                :model-value="botDraft.autoCloseHours ?? ''"
                type="number"
                min="1"
                max="8760"
                placeholder="不自动关闭"
                @update:model-value="botDraft.autoCloseHours = $event ? Number.parseInt($event, 10) : null"
              />
            </AppField>
          </div>

          <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
            <div
              v-for="toggle in [
                { key: 'pinTopicHeader', label: '置顶用户信息卡片', hint: '话题顶部固定一条含用户信息与快捷按钮的消息' },
                { key: 'deleteOriginMessage', label: '撤回用户私聊里的原消息', hint: '命中规则时连同用户那边的原消息一起删除' },
                { key: 'mirrorEdits', label: '镜像编辑', hint: '用户或管理员改动消息时，同步更新对面那条' },
                { key: 'notifyOnUnreachable', label: '用户屏蔽机器人时提醒管理员', hint: '发送失败时在话题里说明原因' },
                { key: 'rulesEnabled', label: '规则引擎', hint: '关闭后所有消息直接中继，不做任何拦截' },
                { key: 'notifyAdmins', label: '通知管理员', hint: '命中与处罚升级时往话题里发告警卡片' },
              ]"
              :key="toggle.key"
              class="flex items-start justify-between gap-3 rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] px-3.5 py-3"
            >
              <div class="min-w-0">
                <p class="text-xs font-medium">{{ toggle.label }}</p>
                <p class="mt-0.5 text-2xs leading-relaxed text-[var(--color-ink-subtle)]">
                  {{ toggle.hint }}
                </p>
              </div>
              <AppSwitch
                v-model="(botDraft as unknown as Record<string, boolean>)[toggle.key]"
                :label="toggle.label"
              />
            </div>
          </div>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <AppField label="相册合并窗口" hint="毫秒">
              <AppInput v-model.number="botDraft.coalesceWindowMs" type="number" min="0" max="5000" />
            </AppField>
            <AppField label="刷屏阈值" hint="3 秒内超过该条数判定为刷屏">
              <AppInput v-model.number="botDraft.floodThreshold" type="number" min="3" max="100" />
            </AppField>
            <AppField label="触发合并的条数" hint="窗口内超过该条数才合并转发">
              <AppInput v-model.number="botDraft.coalesceThreshold" type="number" min="2" max="50" />
            </AppField>
          </div>
        </section>

        <!-- 文案模板 -->
        <section class="space-y-3 border-t border-[var(--color-line-faint)] pt-5">
          <div>
            <h4 class="text-sm font-medium">文案模板</h4>
            <p class="mt-0.5 text-2xs leading-relaxed text-[var(--color-ink-subtle)]">
              支持 Markdown。变量
              <code class="font-mono text-[var(--color-ink-muted)]">
                {name} {username} {id} {ruleName} {matched} {score} {until} {outcome} {botName}
              </code>
              会被替换成实际值 —— 替换值里的特殊字符会自动转义，因此昵称里带 ** 也不会把消息发送搞坏。
            </p>
          </div>

          <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <AppField v-for="tpl in [
              { key: 'greetingText', label: '欢迎语（/start）' },
              { key: 'topicHeaderTemplate', label: '话题头部卡片' },
              { key: 'warnTemplate', label: '警告文案' },
              { key: 'muteTemplate', label: '禁言文案' },
              { key: 'banTemplate', label: '拉黑文案' },
              { key: 'alertCardTemplate', label: '管理员告警卡片' },
            ]" :key="tpl.key" :label="tpl.label">
              <AppTextarea
                v-model="(botDraft as unknown as Record<string, string>)[tpl.key]"
                :rows="4"
                class="text-xs"
              />
            </AppField>
          </div>

          <AppField
            label="静默文案"
            hint="默认为空。静默的意义就是让用户无感知 —— 一旦回复，等于告诉对方「换个号再来」。"
          >
            <AppTextarea v-model="botDraft.silenceTemplate" :rows="2" class="text-xs" placeholder="留空则静默时不发送任何提示" />
          </AppField>
        </section>

        <div class="flex justify-end border-t border-[var(--color-line-faint)] pt-4">
          <AppButton variant="primary" :loading="savingBot" @click="saveBot">保存机器人设置</AppButton>
        </div>
      </div>
    </AppCard>

    <!-- ── 账号 ─────────────────────────────────────────────── -->
    <AppCard>
      <div class="flex items-start justify-between gap-4">
        <div>
          <h3 class="text-base font-semibold tracking-tight">账号</h3>
          <p class="mt-0.5 text-xs text-[var(--color-ink-muted)]">
            修改后其它设备上的登录会立即失效，当前浏览器保持登录
          </p>
        </div>
        <AppBadge v-if="auth.session" tone="accent">{{ auth.session.username }}</AppBadge>
      </div>

      <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <AppField label="当前密码">
          <AppInput v-model="currentPassword" type="password" autocomplete="current-password" />
        </AppField>
        <AppField
          label="新密码"
          hint="至少 8 位"
          :error="passwordTooWeak ? '需要同时包含字母和数字' : null"
        >
          <AppInput
            v-model="newPassword"
            type="password"
            autocomplete="new-password"
            :invalid="passwordTooWeak"
          />
        </AppField>
        <AppField label="确认新密码" :error="passwordMismatch ? '两次输入不一致' : null">
          <AppInput
            v-model="confirmPassword"
            type="password"
            autocomplete="new-password"
            :invalid="passwordMismatch"
          />
        </AppField>
      </div>

      <div class="mt-4 flex justify-end">
        <AppButton
          variant="primary"
          :loading="savingPassword"
          :disabled="!canSubmitPassword"
          @click="changePassword"
        >
          修改密码
        </AppButton>
      </div>
    </AppCard>
  </div>
</template>
