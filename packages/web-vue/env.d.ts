/// <reference types="vite/client" />

// Vue 单文件组件的类型声明。
// vue-tsc 会自己处理 .vue 文件，但 tsc 与编辑器的普通 TS 模式需要这一行，
// 否则 import App from './App.vue' 会报「找不到模块」。
declare module '*.vue' {
  import type { DefineComponent } from 'vue';
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>;
  export default component;
}
