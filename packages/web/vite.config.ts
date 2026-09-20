import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

/**
 * 前端构建配置。
 *
 * 两个刻意的选择：
 *  - 开发期把 `/api` 与 `/ws` 代理到后端（默认 8787），前端代码里因此
 *    可以始终写同源相对路径，不需要区分开发与生产 —— 环境相关的判断
 *    少一处，就少一处「开发能跑、生产白屏」的机会。
 *  - 端口固定成 5173 并在被占用时直接报错（strictPort）：面板调试时
 *    后端要配 CORS 白名单，端口漂移会让这个白名单失效。
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8787',
        changeOrigin: true,
      },
      '/ws': {
        target: process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8787',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    // 把体积大且很少变动的依赖单独拆包：面板更新时用户只需要重新下载
    // 那几百 KB 的业务代码，而不是连 React 一起重下。
    //
    // 用函数形式而不是对象形式：当前 Rollup 类型定义里 manualChunks 只接受
    // 函数（`(id) => name | void`），对象形式会被类型检查拒绝。
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined;
          if (id.includes('/motion') || id.includes('framer-motion')) return 'motion';
          if (id.includes('/react') || id.includes('/scheduler')) return 'react';
          return undefined;
        },
      },
    },
  },
});
