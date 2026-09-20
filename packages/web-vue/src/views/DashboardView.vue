<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';
import { api } from '@/lib/api';
import { compactNumber, formatDelta, relativeTime, HEALTH_LABELS } from '@/lib/format';
import type { Bot, Overview, RuleHit, SessionSummary, TimeseriesPoint } from '@/lib/types';
import { useAsync } from '@/composables/useAsync';
import { useWsEvent } from '@/composables/useWsEvent';
import AppBadge from '@/components/ui/AppBadge.vue';
import AppCard from '@/components/ui/AppCard.vue';
import ContentSwap from '@/components/ui/ContentSwap.vue';
import AppEmpty from '@/components/ui/AppEmpty.vue';
import AppIcon from '@/components/ui/AppIcon.vue';
import AppSkeleton from '@/components/ui/AppSkeleton.vue';
import AnimatedNumber from '@/components/motion/AnimatedNumber.vue';
import Sparkline from '@/components/motion/Sparkline.vue';

/**
 * 仪表盘。
 *
 * 三块内容按「一眼能看出今天有没有出事」排序：KPI 磁贴 → 消息量趋势 →
 * 机器人状态与最近拦截。运维打开面板的第一个问题永远是「有没有异常」，
 * 而不是「帮我做个数据分析」。
 */
const overview = useAsync<Overview>(() => api.get('/api/stats/overview'));
const series = useAsync<{ points: TimeseriesPoint[] }>(() =>
  api.get('/api/stats/timeseries', { days: 14 }),
);
const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'));
const hits = useAsync<{ items: RuleHit[] }>(() => api.get('/api/audit/hits', { limit: 6 }));

// 实时刷新：命中与机器人状态会随时推过来，仪表盘不该等下一次轮询
useWsEvent('rule.hit', () => void hits.reload());
useWsEvent('bot.status', () => {
  void bots.reload();
  void overview.reload();
});
useWsEvent<Overview>('stats.tick', (payload) => {
  overview.data.value = payload;
});

const sparkPoints = computed(() => (series.data.value?.points ?? []).map((p) => p.messagesIn));

/**
 * 只列托管（转发）机器人。
 *
 * 控制台不参与转发，仪表盘上的「机器人」磁贴与这里的列表都是关于
 * 转发链路的 —— 把它列进来会与磁贴的数字对不上（那里已把它排除）。
 */
const hostedBots = computed(() => (bots.data.value?.items ?? []).filter((b) => !b.isManager));

/**
 * 是否值得画折线。
 *
 * 只判断「点数够不够」是不够的：后端会给缺失的日期补 0，所以新装好的
 * 面板会拿到 14 个全为 0 的点 —— 那时画出来的是一条贴着底边的直线，
 * 看起来像坏掉了，而不是像「还没有数据」。
 */
const hasTrendData = computed(
  () => sparkPoints.value.length >= 2 && sparkPoints.value.some((v) => v > 0),
);

const totalIn14d = computed(() =>
  (series.data.value?.points ?? []).reduce((sum, p) => sum + p.messagesIn, 0),
);
const totalBlocked14d = computed(() =>
  (series.data.value?.points ?? []).reduce((sum, p) => sum + p.adsBlocked, 0),
);

const tiles = computed(() => {
  const o = overview.data.value;
  return [
    {
      label: '今日消息',
      value: o?.messages.inToday ?? 0,
      delta: o?.deltas.messagesIn ?? null,
      hint: `累计 ${compactNumber(o?.messages.total ?? 0)} 条`,
      tone: 'neutral' as const,
    },
    {
      label: '今日拦截广告',
      value: o?.ads.blockedToday ?? 0,
      delta: o?.deltas.adsBlocked ?? null,
      hint: `近 24 小时 ${o?.ads.blocked24h ?? 0} 条`,
      tone: 'danger' as const,
    },
    {
      label: '进行中的会话',
      value: o?.sessions.open ?? 0,
      delta: o?.deltas.sessionsCreated ?? null,
      hint: `今日新增 ${o?.sessions.createdToday ?? 0} 个`,
      tone: 'neutral' as const,
    },
    {
      label: '在线机器人',
      value: o?.bots.online ?? 0,
      delta: null,
      hint:
        `共 ${o?.bots.total ?? 0} 个` +
        ((o?.bots.error ?? 0) > 0 ? ` · ${o?.bots.error} 个异常` : ''),
      tone: (o?.bots.error ?? 0) > 0 ? ('danger' as const) : ('success' as const),
    },
  ];
});

/** 首屏的错峰入场：每个磁贴延迟 40ms。上限 4 个，不需要额外的封顶逻辑 */
const stagger = (index: number) => ({
  animation: `tile-in var(--duration-page) var(--ease-expo) ${index * 40}ms both`,
});
</script>

<template>
  <div class="mx-auto w-full max-w-[1440px] space-y-5 p-5 lg:p-6">
    <!-- ── KPI 磁贴 ─────────────────────────────────────────── -->
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <AppCard v-for="(tile, index) in tiles" :key="tile.label" :style="stagger(index)">
        <p class="text-xs text-[var(--color-ink-muted)]">{{ tile.label }}</p>

        <div class="mt-2 flex items-baseline gap-2">
          <AppSkeleton v-if="overview.loading.value && !overview.data.value" height="2rem" width="4rem" />
          <AnimatedNumber
            v-else
            :value="tile.value"
            :format="(v) => compactNumber(Math.round(v))"
            class="text-2xl font-semibold tracking-tight"
            :class="{
              'text-[var(--color-ink)]': tile.tone === 'neutral',
              'text-[var(--color-success)]': tile.tone === 'success',
              'text-[var(--color-danger)]': tile.tone === 'danger',
            }"
          />

          <!-- 环比：null 表示昨日无数据，此时不显示 ——
               显示「—」比显示「+0%」诚实 -->
          <span
            v-if="tile.delta !== null"
            class="text-2xs font-medium"
            :class="{
              'text-[var(--color-success)]': formatDelta(tile.delta).tone === 'up',
              'text-[var(--color-danger)]': formatDelta(tile.delta).tone === 'down',
              'text-[var(--color-ink-subtle)]': formatDelta(tile.delta).tone === 'flat',
            }"
          >
            {{ formatDelta(tile.delta).text }}
          </span>
        </div>

        <p class="mt-1 text-2xs text-[var(--color-ink-subtle)]">{{ tile.hint }}</p>
      </AppCard>
    </div>

    <!-- ── 趋势 + 规则概况 ───────────────────────────────────── -->
    <div class="grid grid-cols-1 gap-3 lg:grid-cols-[1fr_320px]">
      <AppCard>
        <div class="space-y-0.5">
          <h2 class="text-lg font-semibold tracking-tight">近 14 天消息量</h2>
          <p class="text-xs text-[var(--color-ink-muted)]">用户私聊进入话题的中继条数</p>
        </div>

        <div class="mt-4">
          <!--
            骨架 → 内容走交叉淡入，而不是直接替换。

            直接替换的问题是骨架与内容的高度/密度往往不同，
            两者在同一帧切换时眼睛会捕捉到那次突变（「跳一下」）。
            ContentSwap 让它们在 200ms 内重叠过渡，骨架用 absolute 脱出
            文档流，所以容器高度不会在过渡期间等于两者之和。

            这个页面是首屏，值得为它做这一处；其余页面仍是直接替换。
          -->
          <ContentSwap :loading="series.loading.value" :has-data="series.data.value !== null">
            <AppEmpty
              v-if="!hasTrendData"
              compact
              icon="bolt"
              title="还没有足够的数据"
              description="面板运行满两天后这里会显示趋势曲线。"
            />
            <div v-else class="flex items-end justify-between gap-4">
              <div class="min-w-0">
                <div class="flex items-baseline gap-2">
                  <AnimatedNumber
                    :value="totalIn14d"
                    :format="(v) => compactNumber(Math.round(v))"
                    class="text-3xl font-semibold tracking-tight"
                  />
                  <span class="text-xs text-[var(--color-ink-subtle)]">条 / 14 天</span>
                </div>
                <p class="mt-1 text-xs text-[var(--color-ink-muted)]">
                  其中拦截广告
                  <span class="font-medium text-[var(--color-danger)]">{{ totalBlocked14d }}</span>
                  条
                </p>
              </div>
              <Sparkline :points="sparkPoints" :width="280" :height="64" class="shrink-0" />
            </div>

            <template #skeleton>
              <AppSkeleton height="6rem" />
            </template>
          </ContentSwap>
        </div>
      </AppCard>

      <AppCard>
        <h2 class="text-lg font-semibold tracking-tight">规则引擎</h2>
        <div class="mt-4 space-y-2.5">
          <div class="flex items-center justify-between">
            <span class="text-xs text-[var(--color-ink-muted)]">规则总数</span>
            <span class="tnum text-sm font-medium">{{ overview.data.value?.rules.total ?? 0 }}</span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-[var(--color-ink-muted)]">已启用</span>
            <span class="tnum text-sm font-medium text-[var(--color-success)]">
              {{ overview.data.value?.rules.enabled ?? 0 }}
            </span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-[var(--color-ink-muted)]">因异常被停用</span>
            <span
              class="tnum text-sm font-medium"
              :class="
                (overview.data.value?.rules.autoDisabled ?? 0) > 0
                  ? 'text-[var(--color-danger)]'
                  : 'text-[var(--color-ink)]'
              "
            >
              {{ overview.data.value?.rules.autoDisabled ?? 0 }}
            </span>
          </div>
        </div>

        <RouterLink
          v-if="(overview.data.value?.rules.autoDisabled ?? 0) > 0"
          to="/rules"
          class="mt-3 flex items-center gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs text-[var(--color-danger)] transition-opacity duration-[var(--duration-micro)] hover:opacity-80"
        >
          <AppIcon name="warning" :size="14" />
          有规则因执行异常被自动停用，请检查
        </RouterLink>
      </AppCard>
    </div>

    <!-- ── 机器人 + 最近拦截 ─────────────────────────────────── -->
    <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <AppCard flush class="overflow-hidden">
        <div class="px-4 pt-4">
          <div class="flex items-start justify-between gap-4">
            <h2 class="text-lg font-semibold tracking-tight">机器人</h2>
            <RouterLink
              to="/bots"
              class="text-xs text-[var(--color-accent-bright)] hover:underline"
            >
              管理
            </RouterLink>
          </div>
        </div>

        <div class="mt-3">
          <div v-if="bots.loading.value && !bots.data.value" class="space-y-2 p-4">
            <AppSkeleton height="2.25rem" />
            <AppSkeleton height="2.25rem" />
          </div>

          <AppEmpty
            v-else-if="hostedBots.length === 0"
            compact
            icon="bot"
            title="还没有添加机器人"
            description="用 @BotFather 申请一个 token，然后在「机器人」页粘贴进来。"
          />

          <div
            v-else
            v-for="bot in hostedBots"
            :key="bot.id"
            class="border-b border-[var(--color-line-faint)] px-4 py-3 last:border-b-0"
          >
            <div class="flex items-center justify-between gap-3">
              <div class="flex min-w-0 items-center gap-2.5">
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
                <div class="min-w-0">
                  <p class="truncate text-sm font-medium">{{ bot.name }}</p>
                  <code class="font-mono text-xs text-[var(--color-ink-subtle)]">@{{ bot.username }}</code>
                </div>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <AppBadge v-if="!bot.isEnabled" tone="neutral">已停用</AppBadge>
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
              </div>
            </div>
            <p v-if="bot.lastError" class="mt-1 truncate text-2xs text-[var(--color-danger)]">
              {{ bot.lastError }}
            </p>
          </div>
        </div>
      </AppCard>

      <AppCard flush class="overflow-hidden">
        <div class="px-4 pt-4">
          <div class="flex items-start justify-between gap-4">
            <h2 class="text-lg font-semibold tracking-tight">最近的广告拦截</h2>
            <RouterLink
              to="/audit"
              class="text-xs text-[var(--color-accent-bright)] hover:underline"
            >
              全部
            </RouterLink>
          </div>
        </div>

        <div class="mt-3">
          <div v-if="hits.loading.value && !hits.data.value" class="space-y-2 p-4">
            <AppSkeleton height="2.25rem" />
            <AppSkeleton height="2.25rem" />
          </div>

          <AppEmpty
            v-else-if="(hits.data.value?.items.length ?? 0) === 0"
            compact
            icon="rules"
            title="暂无拦截记录"
            description="规则命中后会在这里实时出现。"
          />

          <div
            v-else
            v-for="(hit, index) in hits.data.value?.items"
            :key="hit.id"
            class="border-b border-[var(--color-line-faint)] px-4 py-3 last:border-b-0"
            :class="index === 0 && 'sweep-in'"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0 space-y-0.5">
                <p class="truncate text-sm font-medium">{{ hit.ruleName }}</p>
                <p class="truncate text-xs text-[var(--color-ink-muted)]">
                  {{ hit.contactName }}
                  <span v-if="hit.contactUsername"> · @{{ hit.contactUsername }}</span>
                </p>
              </div>
              <span class="shrink-0 text-2xs text-[var(--color-ink-subtle)]">
                {{ relativeTime(hit.createdAt) }}
              </span>
            </div>
            <code
              v-if="hit.matchedText"
              class="mt-1.5 block truncate font-mono text-xs text-[var(--color-danger)]"
            >
              {{ hit.matchedText }}
            </code>
          </div>
        </div>
      </AppCard>
    </div>
  </div>
</template>

<style scoped>
@keyframes tile-in {
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
