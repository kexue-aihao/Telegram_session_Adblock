<script setup lang="ts">
/**
 * 图标集。
 *
 * 手写内联 SVG 而不是引入图标库：面板一共需要二十来个图标，
 * 一个图标库会带进上千个用不到的路径，而这里全部加起来不到 4KB。
 *
 * 统一规格：24 网格、1.6 描边、round 端点 —— 与页面的圆角语言一致。
 */
import { computed } from 'vue';

const props = withDefaults(
  defineProps<{
    name: IconName;
    size?: number | string;
  }>(),
  { size: 16 },
);

export type IconName =
  | 'dashboard'
  | 'bot'
  | 'sessions'
  | 'rules'
  | 'audit'
  | 'settings'
  | 'plus'
  | 'search'
  | 'close'
  | 'check'
  | 'copy'
  | 'trash'
  | 'edit'
  | 'refresh'
  | 'chevron-right'
  | 'chevron-down'
  | 'logout'
  | 'ban'
  | 'warning'
  | 'command'
  | 'sun'
  | 'moon'
  | 'send'
  | 'info'
  | 'external'
  | 'bolt';

const paths: Record<IconName, string> = {
  dashboard:
    'M3.5 3.5h7v9h-7zM13.5 3.5h7v5.5h-7zM13.5 11.5h7v9h-7zM3.5 15h7v5.5h-7z',
  bot:
    'M4.5 8h15v10a1.5 1.5 0 0 1-1.5 1.5H6A1.5 1.5 0 0 1 4.5 18zM12 8V4.5M9.5 13h.01M14.5 13h.01M2 11.5v4M22 11.5v4',
  sessions:
    'M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5ZM9 11h6M9 14h4',
  rules: 'M12 3 4.5 6.5v5c0 4.6 3.1 8.4 7.5 9.5 4.4-1.1 7.5-4.9 7.5-9.5v-5L12 3Zm-3 9 2 2 4-4',
  audit: 'M5 4.5h9.5L19 9v11.5H5zM14 4.5V9h5M8.5 13h7M8.5 16.5h4.5',
  settings:
    'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM12 2.5v2.2M12 19.3v2.2M21.5 12h-2.2M4.7 12H2.5M18.7 5.3l-1.6 1.6M6.9 17.1l-1.6 1.6M18.7 18.7l-1.6-1.6M6.9 6.9 5.3 5.3',
  plus: 'M12 5v14M5 12h14',
  search: 'M11 4.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM16 16l4 4',
  close: 'M6 6l12 12M18 6 6 18',
  check: 'm5 12.5 4.5 4.5L19 7',
  copy: 'M9 9h9.5A1.5 1.5 0 0 1 20 10.5v9a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 8 19.5v-9A1.5 1.5 0 0 1 9.5 9ZM15 6.5V5.5A2 2 0 0 0 13 3.5H5.5A2 2 0 0 0 3.5 5.5V13a2 2 0 0 0 2 2h1',
  trash:
    'M4 6.5h16M9.5 6.5V4.8A1.3 1.3 0 0 1 10.8 3.5h2.4a1.3 1.3 0 0 1 1.3 1.3v1.7M6.5 6.5 7.4 19a1.5 1.5 0 0 0 1.5 1.4h6.2a1.5 1.5 0 0 0 1.5-1.4l.9-12.5M10.5 10.5v6M13.5 10.5v6',
  edit: 'M16.5 3.9a2.1 2.1 0 0 1 3 3L8.4 18l-4 1 1-4 11.1-11.1Z',
  refresh: 'M20 11.5A8 8 0 1 0 18.4 17M20.5 6.5v5h-5',
  'chevron-right': 'm9 5 7 7-7 7',
  'chevron-down': 'm5 9 7 7 7-7',
  logout:
    'M15 8V5.5A2.5 2.5 0 0 0 12.5 3h-7A2.5 2.5 0 0 0 3 5.5v13A2.5 2.5 0 0 0 5.5 21h7a2.5 2.5 0 0 0 2.5-2.5V16M10 12h11m0 0-3.5-3.5M21 12l-3.5 3.5',
  ban: 'M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17ZM6.5 6.5l11 11',
  warning: 'M12 3.5 2.8 19.5h18.4L12 3.5ZM12 9.5v4.5M12 17h.01',
  command:
    'M9 6.5a2.5 2.5 0 1 0-2.5 2.5H9v6.5a2.5 2.5 0 1 1-2.5-2.5H9m6 0h2.5A2.5 2.5 0 1 1 15 15.5V9m0 0h2.5A2.5 2.5 0 1 0 15 6.5V9m0 0H9',
  sun: 'M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM12 3v1.8M12 19.2V21M21 12h-1.8M4.8 12H3M18.4 5.6l-1.3 1.3M6.9 17.1l-1.3 1.3M18.4 18.4l-1.3-1.3M6.9 6.9 5.6 5.6',
  moon: 'M20.5 14.3A8.5 8.5 0 0 1 9.7 3.5 8.5 8.5 0 1 0 20.5 14.3Z',
  send: 'M20.5 3.5 10.8 13.2M20.5 3.5l-6 17-3.7-7.3-7.3-3.7 17-6Z',
  info: 'M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17ZM12 11v5M12 8h.01',
  external: 'M14 4h6v6M20 4l-8.5 8.5M18 14.5v3a2.5 2.5 0 0 1-2.5 2.5h-9A2.5 2.5 0 0 1 4 17.5v-9A2.5 2.5 0 0 1 6.5 6h3',
  bolt: 'M13.5 2.5 4 14h7l-.5 7.5L20 10h-7l.5-7.5Z',
};

const d = computed(() => paths[props.name] ?? '');
const px = computed(() => (typeof props.size === 'number' ? `${props.size}px` : props.size));
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.6"
    stroke-linecap="round"
    stroke-linejoin="round"
    :style="{ width: px, height: px }"
    aria-hidden="true"
    class="shrink-0"
  >
    <path :d="d" />
  </svg>
</template>
