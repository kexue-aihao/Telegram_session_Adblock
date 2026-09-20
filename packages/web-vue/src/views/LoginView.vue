<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import AppButton from '@/components/ui/AppButton.vue';
import AppField from '@/components/ui/AppField.vue';
import AppInput from '@/components/ui/AppInput.vue';
import AppIcon from '@/components/ui/AppIcon.vue';

/**
 * 登录页。
 *
 * 单管理员密码登录，因此没有「记住我」「忘记密码」这些会引入其他流程的入口。
 *
 * 界面上有一个刻意的取舍：**只有密码，没有用户名**。面板只有一个管理员，
 * 让用户多填一个恒为 admin 的字段只是徒增摩擦。
 */
const auth = useAuthStore();
const router = useRouter();
const route = useRoute();

const password = ref('');
const error = ref<string | null>(null);
const attemptsLeft = ref<number | null>(null);
const submitting = ref(false);
const passwordRef = ref<InstanceType<typeof AppInput> | null>(null);

const canSubmit = computed(() => password.value.length > 0 && !submitting.value);

onMounted(() => {
  passwordRef.value?.$el?.focus?.();
});

async function onSubmit() {
  if (!canSubmit.value) return;

  submitting.value = true;
  error.value = null;

  const result = await auth.login(password.value);
  submitting.value = false;

  if (!result.ok) {
    error.value = result.error ?? '登录失败';
    attemptsLeft.value = result.attemptsLeft ?? null;
    password.value = '';
    return;
  }

  const next = route.query.next;
  await router.replace(typeof next === 'string' ? next : '/');
}
</script>

<template>
  <div class="relative flex h-full items-center justify-center overflow-hidden px-4">
    <!-- 登录页额外叠一层近距离的光晕：它比全局那三团更亮、更近，
         让这张卡片「坐在光里」。这是整站最需要第一印象的一屏。 -->
    <div
      class="pointer-events-none absolute top-1/2 left-1/2 size-[560px] -translate-x-1/2 -translate-y-1/2 rounded-full opacity-25 blur-[100px]"
      style="background: radial-gradient(circle, oklch(0.62 0.19 258), transparent 65%)"
      aria-hidden="true"
    />

    <div
      class="glass-deep relative w-full rounded-3xl p-7 login-card"
      style="max-width: 384px"
    >
      <div class="mb-6 space-y-1.5">
        <div
          class="brand-gradient mb-4 flex size-10 items-center justify-center rounded-2xl shadow-[inset_0_1px_0_rgb(255_255_255/0.28)]"
        >
          <svg viewBox="0 0 24 24" class="size-5 text-white" fill="none" aria-hidden="true">
            <path
              d="M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5Z"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linejoin="round"
            />
          </svg>
        </div>
        <h1 class="text-xl font-semibold tracking-tight">欢迎回来</h1>
        <p class="text-xs text-[var(--color-ink-muted)]">
          登录以管理机器人、会话与广告拦截规则
        </p>
      </div>

      <form class="space-y-4" @submit.prevent="onSubmit">
        <AppField label="管理密码">
          <AppInput
            ref="passwordRef"
            v-model="password"
            type="password"
            placeholder="请输入密码"
            autocomplete="current-password"
            :invalid="Boolean(error)"
            :disabled="submitting"
          />
        </AppField>

        <!-- 错误区域固定高度，避免出错时整个卡片跳动 -->
        <div class="min-h-[18px]">
          <Transition name="err">
            <p v-if="error" class="text-xs text-[var(--color-danger)]">
              {{ error }}
              <span v-if="attemptsLeft !== null && attemptsLeft > 0" class="text-[var(--color-ink-subtle)]">
                · 还可尝试 {{ attemptsLeft }} 次
              </span>
            </p>
          </Transition>
        </div>

        <AppButton type="submit" variant="primary" size="lg" block :loading="submitting" :disabled="!canSubmit">
          登录
        </AppButton>
      </form>

      <p class="mt-5 text-center text-2xs leading-relaxed text-[var(--color-ink-faint)]">
        忘记密码？修改 .env 中的 ADMIN_PASSWORD 后删除数据库里的 admin_users 记录，重启即可重新播种。
      </p>
    </div>
  </div>
</template>

<style scoped>
/* 卡片的入场：从下方 12px 浮起并轻微放大。
   这是整站唯一一处用 480ms 的入场 —— 首屏值得慢一点。 */
.login-card {
  animation: login-in var(--duration-page) var(--ease-expo) both;
}

@keyframes login-in {
  from {
    opacity: 0;
    transform: translateY(12px) scale(0.99);
  }
  to {
    opacity: 1;
    transform: translateY(0) scale(1);
  }
}

.err-enter-active {
  transition:
    opacity var(--duration-state) var(--ease-state),
    transform var(--duration-state) var(--ease-expo);
}
.err-enter-from {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
