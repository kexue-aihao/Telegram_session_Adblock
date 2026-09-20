import vue from '@vitejs/plugin-vue';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vite';

/**
 * 前端构建配置。
 *
 * 与 React 版的取向一致，但代理目标指向 Go 服务端（默认 8787）。
 * 开发期把 /api 与 /ws 代理过去，前端代码里因此可以始终写同源相对路径 ——
 * 环境相关的判断少一处，就少一处「开发能跑、生产白屏」的机会。
 */
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5273,
    strictPort: true,
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8787',
        changeOrigin: true,
      },
      '/ws': {
        target: process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8787',
        ws: true,
        // 刻意不开 changeOrigin：它会把 Host 改写成上游地址，
        // 而 Origin 仍是浏览器的 localhost:5273 —— 服务端的同源校验
        // 比较的正是这两者，改写后必然握手失败。
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    // 把体积大且很少变动的依赖单独拆包：面板更新时用户只需要重新下载
    // 那几百 KB 的业务代码，而不是连 Vue 一起重下。
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined;
          if (id.includes('/vue/') || id.includes('/@vue/')) return 'vue';
          return undefined;
        },
      },
    },
  },
});
