<script setup lang="ts">
import { computed, ref } from 'vue';
import { api, ApiError } from '@/lib/api';
import { relativeTime, splitHighlight, MATCH_MODE_LABELS, TARGET_LABELS, ACTION_LABELS } from '@/lib/format';
import type { AdRule, RuleTestResult } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useToastStore } from '@/stores/toast';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppButton from '@/components/ui/AppButton.vue';
import AppCard from '@/components/ui/AppCard.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppSwitch from '@/components/ui/AppSwitch.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';
import RuleEditor from '@/components/rules/RuleEditor.vue';

/**
 * 规则页。
 *
 * 列表本身很普通，**正则测试沙盒**才是这个页面的核心价值：
 * 正则的写法与匹配结果之间的关系对多数运维并不直观，让他们
 * 「保存后再看线上效果」等于拿真实用户做实验。
 */
const toast = useToastStore();
const rules = useAsync<{ items: AdRule[] }>(() => api.get('/api/rules'));

const editorOpen = ref(false);
const editing = ref<AdRule | null>(null);

const grouped = computed(() => {
  const items = rules.data.value?.items ?? [];
  return {
    custom: items.filter((r) => !r.isSystem),
    system: items.filter((r) => r.isSystem),
  };
});

/**
 * 列表里怎么展示 pattern。
 *
 * 共现规则的 pattern 是阈值整数而不是正则，仍然套 `/3/` 那副壳子等于告诉
 * 管理员「这是一条匹配字面量 3 的正则」。沙盒那边已经专门讲过它不匹配文本，
 * 这里得是同一个说法。
 */
const patternDisplay = (rule: AdRule) =>
  rule.matchMode === 'cooccurrence'
    ? `同一条消息命中 ${rule.pattern} 条不同规则时触发`
    : `/${rule.pattern}/${rule.flags}`;

const stagger = (index: number) => ({
  animation: `row-in var(--duration-layout) var(--ease-expo) ${Math.min(index, 12) * 30}ms both`,
});

async function toggle(rule: AdRule, next: boolean) {
  // 乐观更新：开关的状态切换必须立刻可见，等一次网络往返会让它显得迟钝
  const previous = rule.isEnabled;
  rule.isEnabled = next;

  try {
    await api.post(`/api/rules/${rule.id}/toggle`, { isEnabled: next });
  } catch (err) {
    rule.isEnabled = previous;
    toast.error('切换失败', err instanceof ApiError ? err.message : '未知错误');
  }
}

function onCreate() {
  editing.value = null;
  editorOpen.value = true;
}

function onEdit(rule: AdRule) {
  editing.value = rule;
  editorOpen.value = true;
}

async function onDelete(rule: AdRule) {
  if (!confirm(`确定删除规则「${rule.name}」？\n\n已产生的命中记录不会被删除 —— 审计里保存了规则的完整快照。`)) {
    return;
  }
  try {
    await api.delete(`/api/rules/${rule.id}`);
    toast.success('规则已删除', '历史命中记录仍然保留');
    void rules.reload();
  } catch (err) {
    toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
  }
}

function onSaved() {
  editorOpen.value = false;
  void rules.reload();
}
</script>

<template>
  <div class="mx-auto w-full max-w-[1200px] space-y-5 p-5 lg:p-6">
    <div class="flex items-start justify-between gap-4">
      <div class="space-y-0.5">
        <h2 class="text-lg font-semibold tracking-tight">广告拦截规则</h2>
        <p class="max-w-2xl text-xs leading-relaxed text-[var(--color-ink-muted)]">
          消息在转发进话题之前先经过规则引擎；命中即拦截，不会留下能被截图的时间窗。
          规则按优先级从小到大依次匹配，命中多条时动作取并集。
        </p>
      </div>
      <AppButton variant="primary" @click="onCreate">
        <AppIcon name="plus" :size="16" />
        新建规则
      </AppButton>
    </div>

    <div v-if="rules.loading.value && !rules.data.value" class="space-y-2">
      <AppSkeleton v-for="i in 3" :key="i" height="5rem" radius="var(--radius-2xl)" />
    </div>

    <template v-else>
      <section v-for="group in [
        { title: '自定义规则', desc: '你自己添加的规则，优先级与启停都可以自由调整。', items: grouped.custom, showCreate: true },
        { title: '预置规则', desc: '系统自带的基础规则，可以停用或修改，但不建议删除。', items: grouped.system, showCreate: false },
      ]" :key="group.title" class="space-y-2">
        <div class="space-y-0.5">
          <h3 class="text-sm font-medium">{{ group.title }}</h3>
          <p class="text-2xs text-[var(--color-ink-subtle)]">{{ group.desc }}</p>
        </div>

        <AppCard v-if="group.items.length === 0" flush>
          <AppEmpty
            icon="rules"
            compact
            title="还没有自定义规则"
            description="预置规则已经能拦下大部分常见广告；再补充几条针对你自己业务的规则会更准。"
          >
            <AppButton v-if="group.showCreate" size="sm" variant="primary" @click="onCreate">
              <AppIcon name="plus" :size="14" />
              新建规则
            </AppButton>
          </AppEmpty>
        </AppCard>

        <div v-else class="space-y-2">
          <AppCard
            v-for="(rule, index) in group.items"
            :key="rule.id"
            :style="stagger(index)"
            class="transition-opacity duration-[var(--duration-state)]"
            :class="!rule.isEnabled && 'opacity-55'"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0 flex-1 space-y-1.5">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-medium">{{ rule.name }}</span>
                  <AppBadge>{{ MATCH_MODE_LABELS[rule.matchMode] }}</AppBadge>
                  <AppBadge :tone="rule.action === 'escalate' ? 'danger' : 'accent'">
                    {{ ACTION_LABELS[rule.action] }}
                  </AppBadge>
                  <AppBadge>优先级 {{ rule.priority }}</AppBadge>
                  <AppBadge v-if="rule.hitCount > 0" tone="warn">命中 {{ rule.hitCount }} 次</AppBadge>
                  <AppBadge v-if="rule.autoDisabled" tone="danger">已自动停用</AppBadge>
                </div>

                <code
                  class="block truncate rounded-lg bg-[var(--color-bg-2)] px-2 py-1.5 font-mono text-xs text-[var(--color-ink-muted)]"
                >
                  {{ patternDisplay(rule) }}
                </code>

                <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-ink-subtle)]">
                  <span>目标：{{ TARGET_LABELS[rule.target] }}</span>
                  <span>违规分 +{{ rule.severity }}</span>
                  <span v-if="rule.botId !== null">仅对机器人 #{{ rule.botId }} 生效</span>
                  <span v-if="rule.hitCount > 0">最近命中 {{ relativeTime(rule.lastHitAt) }}</span>
                </div>

                <p v-if="rule.note" class="text-2xs leading-relaxed text-[var(--color-ink-subtle)]">
                  {{ rule.note }}
                </p>
              </div>

              <div class="flex shrink-0 flex-col items-end gap-2">
                <AppSwitch
                  :model-value="rule.isEnabled"
                  :label="`启用规则 ${rule.name}`"
                  @update:model-value="(v) => toggle(rule, v)"
                />
                <div class="flex items-center gap-0.5">
                  <AppButton size="sm" variant="ghost" aria-label="编辑" @click="onEdit(rule)">
                    <AppIcon name="edit" :size="14" />
                  </AppButton>
                  <AppButton
                    size="sm"
                    variant="ghost"
                    class="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
                    aria-label="删除"
                    @click="onDelete(rule)"
                  >
                    <AppIcon name="trash" :size="14" />
                  </AppButton>
                </div>
              </div>
            </div>
          </AppCard>
        </div>
      </section>
    </template>

    <RuleEditor :open="editorOpen" :rule="editing" @close="editorOpen = false" @saved="onSaved" />
  </div>
</template>

<style scoped>
@keyframes row-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
