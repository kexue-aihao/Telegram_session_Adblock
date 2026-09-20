<script setup lang="ts">
import { ref, watch } from 'vue';
import { api, ApiError } from '@/lib/api';
import { ACTION_LABELS, MATCH_MODE_LABELS, TARGET_LABELS } from '@/lib/format';
import type { AdRule } from '@/lib/types';
import { useToastStore } from '@/stores/toast';
import AppButton from '@/components/ui/AppButton.vue';
import AppField from '@/components/ui/AppField.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppSelect from '@/components/ui/AppSelect.vue';
import AppSwitch from '@/components/ui/AppSwitch.vue';
import AppTextarea from '@/components/ui/AppTextarea.vue';
import RegexSandbox from './RegexSandbox.vue';

/**
 * 规则编辑器（右侧抽屉）。
 *
 * 用抽屉而不是模态：调规则时经常要对照左侧列表里其他规则的写法，
 * 模态会把整个列表遮住。
 */
const props = defineProps<{ open: boolean; rule: AdRule | null }>();
const emit = defineEmits<{ close: []; saved: [] }>();

const toast = useToastStore();
const saving = ref(false);

interface Draft {
  name: string;
  pattern: string;
  flags: string;
  matchMode: string;
  target: string;
  action: string;
  severity: number;
  priority: number;
  isEnabled: boolean;
  note: string;
}

function emptyDraft(): Draft {
  return {
    name: '',
    pattern: '',
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'delete',
    severity: 10,
    priority: 100,
    isEnabled: true,
    note: '',
  };
}

const draft = ref<Draft>(emptyDraft());

watch(
  () => [props.open, props.rule] as const,
  ([open, rule]) => {
    if (!open) return;
    draft.value = rule
      ? {
          name: rule.name,
          pattern: rule.pattern,
          flags: rule.flags,
          matchMode: rule.matchMode,
          target: rule.target,
          action: rule.action,
          severity: rule.severity,
          priority: rule.priority,
          isEnabled: rule.isEnabled,
          note: rule.note ?? '',
        }
      : emptyDraft();
  },
  { immediate: true },
);

async function onSave() {
  saving.value = true;
  const payload = {
    name: draft.value.name.trim(),
    pattern: draft.value.pattern,
    flags: draft.value.flags,
    matchMode: draft.value.matchMode,
    target: draft.value.target,
    action: draft.value.action,
    severity: draft.value.severity,
    priority: draft.value.priority,
    isEnabled: draft.value.isEnabled,
    note: draft.value.note.trim() || null,
  };

  try {
    if (props.rule) {
      await api.patch(`/api/rules/${props.rule.id}`, payload);
      toast.success('规则已更新');
    } else {
      await api.post('/api/rules', payload);
      toast.success('规则已创建');
    }
    emit('saved');
  } catch (err) {
    toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div v-if="open" class="fixed inset-0 z-50">
        <div class="absolute inset-0 bg-black/50 backdrop-blur-[2px]" @click="emit('close')" />

        <aside
          class="glass-deep absolute top-0 right-0 bottom-0 flex w-full flex-col border-l border-[var(--color-line)]"
          style="max-width: 620px"
          role="dialog"
          aria-modal="true"
          :aria-label="rule ? '编辑规则' : '新建规则'"
        >
          <header
            class="flex items-center justify-between gap-4 border-b border-[var(--color-line-faint)] px-5 py-4"
          >
            <h3 class="text-base font-semibold tracking-tight">
              {{ rule ? '编辑规则' : '新建规则' }}
            </h3>
            <button
              type="button"
              aria-label="关闭"
              class="rounded-lg p-1.5 text-[var(--color-ink-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]"
              @click="emit('close')"
            >
              <svg viewBox="0 0 16 16" class="size-4" fill="none" aria-hidden="true">
                <path
                  d="M4 4l8 8M12 4l-8 8"
                  stroke="currentColor"
                  stroke-width="1.6"
                  stroke-linecap="round"
                />
              </svg>
            </button>
          </header>

          <div class="flex-1 space-y-5 overflow-y-auto px-5 py-4">
            <AppField label="规则名称" hint="给自己看的，写清楚它拦的是什么">
              <AppInput v-model="draft.name" placeholder="例如：游戏代练广告" />
            </AppField>

            <RegexSandbox
              :pattern="draft.pattern"
              :flags="draft.flags"
              :match-mode="draft.matchMode"
              @update:pattern="draft.pattern = $event"
              @update:flags="draft.flags = $event"
            />

            <div class="grid grid-cols-2 gap-3">
              <AppField label="匹配方式">
                <AppSelect v-model="draft.matchMode">
                  <option v-for="(label, value) in MATCH_MODE_LABELS" :key="value" :value="value">
                    {{ label }}
                  </option>
                </AppSelect>
              </AppField>

              <AppField label="匹配目标" hint="选择消息的哪一部分参与匹配">
                <AppSelect v-model="draft.target">
                  <option v-for="(label, value) in TARGET_LABELS" :key="value" :value="value">
                    {{ label }}
                  </option>
                </AppSelect>
              </AppField>
            </div>

            <div class="grid grid-cols-3 gap-3">
              <AppField label="命中动作">
                <AppSelect v-model="draft.action">
                  <option v-for="(label, value) in ACTION_LABELS" :key="value" :value="value">
                    {{ label }}
                  </option>
                </AppSelect>
              </AppField>

              <AppField label="违规分" hint="累加到阶梯处罚">
                <AppInput v-model.number="draft.severity" type="number" min="0" max="1000" />
              </AppField>

              <AppField label="优先级" hint="越小越先匹配">
                <AppInput v-model.number="draft.priority" type="number" min="0" max="10000" />
              </AppField>
            </div>

            <AppField label="备注" hint="可选">
              <AppTextarea
                v-model="draft.note"
                :rows="2"
                placeholder="记录这条规则的来历，方便日后回头改"
              />
            </AppField>

            <div
              class="flex items-center justify-between rounded-xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] px-3.5 py-3"
            >
              <div>
                <p class="text-xs font-medium">启用这条规则</p>
                <p class="text-2xs text-[var(--color-ink-subtle)]">
                  停用的规则不参与匹配，但会保留全部配置
                </p>
              </div>
              <AppSwitch v-model="draft.isEnabled" label="启用规则" />
            </div>
          </div>

          <footer
            class="flex items-center justify-end gap-2 border-t border-[var(--color-line-faint)] px-5 py-3.5"
          >
            <AppButton variant="ghost" :disabled="saving" @click="emit('close')">取消</AppButton>
            <AppButton
              variant="primary"
              :loading="saving"
              :disabled="!draft.name.trim() || !draft.pattern"
              @click="onSave"
            >
              {{ rule ? '保存修改' : '创建规则' }}
            </AppButton>
          </footer>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* 抽屉从右侧滑入。缓动比默认的 ease-expo 略硬一点，
   让「推出来」这个动作显得果断而不是飘。 */
.drawer-enter-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.drawer-enter-active aside {
  transition: transform var(--duration-layout) cubic-bezier(0.22, 1, 0.36, 1);
}
.drawer-leave-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.drawer-leave-active aside {
  transition: transform var(--duration-state) var(--ease-state);
}
.drawer-enter-from,
.drawer-leave-to {
  opacity: 0;
}
.drawer-enter-from aside,
.drawer-leave-to aside {
  transform: translateX(100%);
}
</style>
