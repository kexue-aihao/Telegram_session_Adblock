<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { api, ApiError } from '@/lib/api';
import type { BotValidation, GroupCheck } from '@/lib/types';
import { useToastStore } from '@/stores/toast';
import AppButton from '@/components/ui/AppButton.vue';
import AppField from '@/components/ui/AppField.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppModal from '@/components/ui/AppModal.vue';

/**
 * 创建机器人向导。
 *
 * 复刻 @BotFather 的三步，但每一步都给出**可执行的**提示。
 * 这个流程里的失败几乎全是 Telegram 侧的配置问题（没开 Topics、
 * 没给管理员权限、privacy mode 没关），一句「校验失败」会让用户
 * 去猜半小时 —— 所以每一步都把「怎么做」写在界面上。
 *
 * mode = 'manager' 是另一条路径：绑定**控制台机器人**。它不参与转发，
 * 因此没有管理群这一步，一步就完事。
 */
const props = defineProps<{ open: boolean; mode?: 'relay' | 'manager' }>();
const emit = defineEmits<{ close: []; created: [] }>();

const isManagerMode = computed(() => props.mode === 'manager');

const toast = useToastStore();

type Step = 'token' | 'group';

const step = ref<Step>('token');
const token = ref('');
const validating = ref(false);
const validation = ref<BotValidation | null>(null);

const groupId = ref('');
const checkingGroup = ref(false);
const groupCheck = ref<GroupCheck | null>(null);
const creating = ref(false);

watch(
  () => props.open,
  (open) => {
    if (!open) return;
    // 每次打开都从第一步开始，避免残留上一次的 token
    step.value = 'token';
    token.value = '';
    validation.value = null;
    groupId.value = '';
    groupCheck.value = null;
  },
);

const stepIndex = computed(() => (step.value === 'token' ? 1 : 2));
const stepCount = computed(() => (isManagerMode.value ? 1 : 2));

async function onValidate() {
  validating.value = true;
  validation.value = null;
  try {
    const result = await api.post<BotValidation>('/api/bots/validate', {
      token: token.value.trim(),
    });
    validation.value = result;
    if (result.ok) toast.success('Token 有效', `机器人 @${result.username}`);
  } catch (err) {
    toast.error('校验失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    validating.value = false;
  }
}

async function onCheckGroup() {
  const parsed = Number.parseInt(groupId.value.trim(), 10);
  if (!Number.isFinite(parsed) || parsed >= 0) {
    toast.error('群 ID 格式不对', '超级群 ID 是负数，形如 -1001234567890');
    return;
  }

  checkingGroup.value = true;
  try {
    groupCheck.value = await api.post<GroupCheck>('/api/bots/check-group', {
      token: token.value.trim(),
      chatId: parsed,
    });
  } catch (err) {
    toast.error('体检失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    checkingGroup.value = false;
  }
}

async function onCreate() {
  creating.value = true;
  try {
    const parsed = groupId.value.trim() ? Number.parseInt(groupId.value.trim(), 10) : null;
    await api.post('/api/bots', {
      token: token.value.trim(),
      adminGroupId: Number.isFinite(parsed) ? parsed : null,
      name: validation.value?.name ?? undefined,
      manager: isManagerMode.value,
    });
    if (isManagerMode.value) {
      toast.success(
        '管理机器人已绑定',
        '在 Telegram 里私聊它发 /start —— 它只做管理，不参与转发',
      );
    } else {
      toast.success('机器人已创建', '正在启动长轮询，状态会实时更新');
    }
    emit('created');
  } catch (err) {
    toast.error('绑定失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    creating.value = false;
  }
}
</script>

<template>
  <AppModal
    :open="open"
    :title="isManagerMode ? '绑定管理机器人' : step === 'token' ? '创建机器人' : '绑定管理群'"
    :description="
      isManagerMode ? '它只做管理，不参与消息转发' : `第 ${stepIndex} 步 / 共 ${stepCount} 步`
    "
    width="34rem"
    @close="emit('close')"
  >
    <Transition name="step" mode="out-in">
      <div v-if="step === 'token'" key="token" class="space-y-4">
        <!-- 控制台与管理群无关，所以在管理模式下换一套说明：
             贴一段「先去建群、关 Privacy Mode」在这里只会让人白忙 -->
        <div
          v-if="isManagerMode"
          class="space-y-2 rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] p-3.5"
        >
          <p class="text-xs font-medium">管理机器人是什么</p>
          <p class="text-2xs leading-relaxed text-[var(--color-ink-muted)]">
            它是你在 Telegram 里的<strong>操作入口</strong>：私聊它发
            <code class="font-mono">/start</code>
            就能增删托管其他机器人，不必回面板。
            <br />
            它<strong>不参与转发</strong> —— 谁给它发消息都不会变成话题。
            所以它需要单独在 @BotFather 里创建，并且不需要建群、不需要关 Privacy Mode。
          </p>
        </div>

        <div v-else class="space-y-2 rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] p-3.5">
          <p class="text-xs font-medium">在 @BotFather 里这样操作</p>
          <ol class="space-y-1 text-2xs leading-relaxed text-[var(--color-ink-muted)]">
            <li>1. 打开 @BotFather，发送 /newbot</li>
            <li>2. 依次输入机器人名称与用户名（用户名必须以 bot 结尾）</li>
            <li>3. 复制它返回的那串 token，粘贴到下面</li>
            <li>
              4. 建议再发送 /setprivacy 并选择 <strong>Disable</strong> ——
              否则机器人读不到群里的普通消息
            </li>
          </ol>
        </div>

        <AppField
          label="Bot Token"
          hint="形如 123456789:AAF…"
          :error="validation && !validation.ok ? validation.error : null"
        >
          <AppInput
            v-model="token"
            placeholder="123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
            autocomplete="off"
            spellcheck="false"
            class="font-mono text-xs"
            :invalid="Boolean(validation && !validation.ok)"
          />
        </AppField>

        <Transition name="step">
          <div
            v-if="validation?.ok"
            class="space-y-1.5 rounded-xl bg-[var(--color-success-soft)] px-3.5 py-3"
          >
            <p class="text-xs font-medium text-[var(--color-success)]">
              ✅ 已验证：{{ validation.name }}（@{{ validation.username }}）
            </p>
            <!-- Privacy Mode 是转发路径上最常见的一个坑：不关掉的话
                 机器人读不到群里的普通消息，话题中继会完全失效。
                 但控制台不读群消息，这条提醒对它不适用。 -->
            <p
              v-if="!isManagerMode && validation.canReadAllGroupMessages === false"
              class="text-2xs leading-relaxed text-[var(--color-warn)]"
            >
              ⚠️ 该机器人处于 Privacy Mode，读不到群里的普通消息。请到 @BotFather 发送
              /setprivacy → 选择 Disable，否则话题中继无法工作。
            </p>
          </div>
        </Transition>
      </div>

      <div v-else key="group" class="space-y-4">
        <div
          class="space-y-2 rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] p-3.5"
        >
          <p class="text-xs font-medium">绑定前请在 Telegram 里准备好</p>
          <ol class="space-y-1 text-2xs leading-relaxed text-[var(--color-ink-muted)]">
            <li>1. 建一个私有超级群，在群设置里开启「话题 / Topics」</li>
            <li>2. 把机器人拉进群，并设为管理员</li>
            <li>
              3. 至少勾选「管理话题」与「删除消息」两项权限 ——
              前者用于建话题，后者用于撤回命中的广告
            </li>
            <li>4. 获取群 ID：把群消息转发给 @userinfobot，或看群链接</li>
          </ol>
        </div>

        <AppField label="管理群 ID" hint="超级群 ID 是负数，形如 -1001234567890">
          <AppInput v-model="groupId" placeholder="-1001234567890" class="font-mono text-xs" />
        </AppField>

        <AppButton size="sm" :loading="checkingGroup" :disabled="!groupId.trim()" @click="onCheckGroup">
          体检这个群
        </AppButton>

        <Transition name="step">
          <div
            v-if="groupCheck"
            class="space-y-1.5 rounded-xl px-3.5 py-3"
            :class="groupCheck.ok ? 'bg-[var(--color-success-soft)]' : 'bg-[var(--color-warn-soft)]'"
          >
            <p
              class="text-xs font-medium"
              :class="groupCheck.ok ? 'text-[var(--color-success)]' : 'text-[var(--color-warn)]'"
            >
              {{ groupCheck.ok ? `✅ ${groupCheck.title ?? '管理群'} 配置正常` : `⚠️ ${groupCheck.title ?? '管理群'} 还有问题` }}
            </p>
            <p
              v-for="problem in groupCheck.problems"
              :key="problem"
              class="text-2xs leading-relaxed text-[var(--color-ink-muted)]"
            >
              · {{ problem }}
            </p>
          </div>
        </Transition>
      </div>
    </Transition>

    <template #footer>
      <template v-if="step === 'token'">
        <AppButton variant="ghost" @click="emit('close')">取消</AppButton>

        <!-- 管理模式只有一步：验证完直接绑定 -->
        <AppButton
          v-if="!validation?.ok"
          variant="primary"
          :loading="validating"
          :disabled="token.trim().length < 10"
          @click="onValidate"
        >
          验证 Token
        </AppButton>
        <AppButton
          v-else-if="isManagerMode"
          variant="primary"
          :loading="creating"
          @click="onCreate"
        >
          完成绑定
        </AppButton>
        <AppButton v-else variant="primary" @click="step = 'group'">
          下一步
          <AppIcon name="chevron-right" :size="14" />
        </AppButton>
      </template>

      <template v-else>
        <AppButton variant="ghost" @click="step = 'token'">上一步</AppButton>
        <AppButton variant="primary" :loading="creating" @click="onCreate">
          {{ groupId.trim() ? '完成创建' : '稍后再绑定群' }}
        </AppButton>
      </template>
    </template>
  </AppModal>
</template>

<style scoped>
/* 步骤切换：横向 12px 位移，方向与「前进/后退」一致。
   用纯横向而不是叠加淡入淡出，让「步骤在推进」这件事更明确。 */
.step-enter-active {
  transition:
    opacity var(--duration-layout) var(--ease-expo),
    transform var(--duration-layout) var(--ease-expo);
}
.step-leave-active {
  transition:
    opacity var(--duration-state) var(--ease-state),
    transform var(--duration-state) var(--ease-state);
}
.step-enter-from {
  opacity: 0;
  transform: translateX(12px);
}
.step-leave-to {
  opacity: 0;
  transform: translateX(-12px);
}
</style>
