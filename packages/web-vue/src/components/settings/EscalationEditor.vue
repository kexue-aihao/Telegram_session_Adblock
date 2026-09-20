<script setup lang="ts">
import { computed } from 'vue';
import type { EscalationConfig, EscalationStep } from '@/lib/types';
import AppButton from '@/components/ui/AppButton.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppSelect from '@/components/ui/AppSelect.vue';
import AppSwitch from '@/components/ui/AppSwitch.vue';

/**
 * 阶梯处罚编辑器。
 *
 * 每一档都给出「这一档会发生什么」的人话说明，而不只是几个数字输入框 ——
 * 这几档直接决定用户会不会被误伤，管理员必须清楚自己正在配置什么。
 *
 * 按分数排序展示（而不是按数组顺序）：档位的意义就是「分数越高越严」，
 * 展示顺序与语义顺序不一致会让人反复确认。
 */
const model = defineModel<EscalationConfig>({ required: true });

const sorted = computed(() => [...model.value.steps].sort((a, b) => a.atScore - b.atScore));

const SANCTION_OPTIONS = [
  { value: 'warn', label: '私聊警告' },
  { value: 'silence', label: '静默（消息不再进话题，用户无感知）' },
  { value: 'mute', label: '硬禁言（回复禁言提示与解禁时间）' },
  { value: 'ban', label: '拉黑（机器人不再响应，话题自动关闭）' },
];

function update(id: string, patch: Partial<EscalationStep>) {
  model.value = {
    ...model.value,
    steps: model.value.steps.map((s) => (s.id === id ? { ...s, ...patch } : s)),
  };
}

function remove(id: string) {
  model.value = { ...model.value, steps: model.value.steps.filter((s) => s.id !== id) };
}

function add() {
  const last = sorted.value[sorted.value.length - 1];
  model.value = {
    ...model.value,
    steps: [
      ...model.value.steps,
      {
        id: `step-${Date.now()}`,
        atScore: (last?.atScore ?? 0) + 3,
        type: 'warn',
        durationMinutes: null,
        deleteMessage: true,
        notifyAdmins: true,
        enabled: true,
      },
    ],
  };
}
</script>

<template>
  <div class="space-y-2">
    <TransitionGroup name="step">
      <div
        v-for="(step, index) in sorted"
        :key="step.id"
        class="rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] p-3"
      >
        <div class="flex items-start gap-3">
          <div
            class="flex size-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-active)] text-2xs font-medium text-[var(--color-ink-muted)]"
          >
            {{ index + 1 }}
          </div>

          <div class="min-w-0 flex-1 space-y-2.5">
            <div class="flex flex-wrap items-end gap-2.5">
              <label class="space-y-1">
                <span class="block text-2xs text-[var(--color-ink-subtle)]">达到分数</span>
                <AppInput
                  :model-value="step.atScore"
                  type="number"
                  min="1"
                  max="10000"
                  class="h-8 w-24 text-xs"
                  @update:model-value="update(step.id, { atScore: Number.parseInt($event, 10) || 1 })"
                />
              </label>

              <label class="min-w-[220px] flex-1 space-y-1">
                <span class="block text-2xs text-[var(--color-ink-subtle)]">处置方式</span>
                <AppSelect
                  :model-value="step.type"
                  class="h-8 text-xs"
                  @update:model-value="
                    update(step.id, {
                      type: $event as EscalationStep['type'],
                      // 只有禁言需要时长，切走时清掉避免留下无意义的数字
                      durationMinutes: $event === 'mute' ? (step.durationMinutes ?? 1440) : null,
                    })
                  "
                >
                  <option v-for="opt in SANCTION_OPTIONS" :key="opt.value" :value="opt.value">
                    {{ opt.label }}
                  </option>
                </AppSelect>
              </label>

              <label v-if="step.type === 'mute'" class="space-y-1">
                <span class="block text-2xs text-[var(--color-ink-subtle)]">时长（分钟）</span>
                <AppInput
                  :model-value="step.durationMinutes ?? 1440"
                  type="number"
                  min="1"
                  max="525600"
                  class="h-8 w-28 text-xs"
                  @update:model-value="
                    update(step.id, { durationMinutes: Number.parseInt($event, 10) || null })
                  "
                />
              </label>

              <div class="ml-auto flex items-center gap-2">
                <AppSwitch
                  :model-value="step.enabled"
                  label="启用这一档"
                  @update:model-value="(v) => update(step.id, { enabled: v })"
                />
                <AppButton
                  size="sm"
                  variant="ghost"
                  class="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
                  aria-label="删除这一档"
                  :disabled="model.steps.length <= 1"
                  @click="remove(step.id)"
                >
                  <AppIcon name="trash" :size="14" />
                </AppButton>
              </div>
            </div>

            <!-- 这些提示不是装饰：静默与永久禁言都是「用户会感到莫名其妙」
                 的处置，必须在配置时就说清楚 -->
            <p v-if="step.type === 'silence'" class="text-2xs leading-relaxed text-[var(--color-warn)]">
              静默用户不会收到任何提示，消息直接石沉大海。它最不打扰人，
              但也最容易让被误伤的用户困惑 —— 建议只在已明确警告过之后使用。
            </p>
            <p
              v-if="step.type === 'mute' && step.durationMinutes === null"
              class="flex items-center gap-1.5 text-2xs text-[var(--color-warn)]"
            >
              <AppIcon name="warning" :size="13" />
              时长为空表示永久禁言，请确认这是你想要的。
            </p>
          </div>
        </div>
      </div>
    </TransitionGroup>

    <AppButton size="sm" variant="secondary" :disabled="model.steps.length >= 20" @click="add">
      <AppIcon name="plus" :size="14" />
      添加一档
    </AppButton>
  </div>
</template>

<style scoped>
/* 增删档位时其余平滑让位，而不是生硬地跳一下 */
.step-enter-active,
.step-leave-active {
  transition: all var(--duration-layout) var(--ease-expo);
}
.step-enter-from {
  opacity: 0;
  transform: translateY(6px);
}
.step-leave-to {
  opacity: 0;
  transform: translateX(-8px);
}
.step-move {
  transition: transform var(--duration-layout) var(--ease-expo);
}
</style>
