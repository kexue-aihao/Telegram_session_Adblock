/**
 * 用 Chrome DevTools Protocol 给面板截图。
 *
 * 不装 playwright/puppeteer：Node 24 内置了 WebSocket 客户端，
 * 而 CDP 就是一层 JSON over WebSocket —— 为一个截图需求拉进来
 * 几百 MB 的浏览器二进制与依赖并不划算。
 *
 * 用法：node scripts/shoot.mjs <输出目录>
 */

import { spawn } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { setTimeout as sleep } from 'node:timers/promises';

const OUT_DIR = process.argv[2] ?? './shots';
const BASE = process.env.PREVIEW_URL ?? 'http://localhost:5273';
const PASSWORD = process.env.PREVIEW_PASSWORD ?? 'preview1234';
const PORT = 9222;

const CHROME_CANDIDATES = [
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
];

mkdirSync(OUT_DIR, { recursive: true });

// ── 启动 Chrome ────────────────────────────────────────────────
const profileDir = `${OUT_DIR}/.chrome-profile`;
rmSync(profileDir, { recursive: true, force: true });

const chrome = spawn(
  CHROME_CANDIDATES[0],
  [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profileDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-extensions',
    '--hide-scrollbars',
    // 固定设备缩放比，让截图尺寸可预期
    '--force-device-scale-factor=2',
    '--window-size=1600,1000',
    'about:blank',
  ],
  { stdio: 'ignore' },
);

process.on('exit', () => chrome.kill());

// ── 等待调试端口就绪 ────────────────────────────────────────────
async function waitForDevTools() {
  for (let i = 0; i < 40; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      if (res.ok) return;
    } catch {
      // 还没起来
    }
    await sleep(250);
  }
  throw new Error('Chrome 调试端口未就绪');
}

await waitForDevTools();

// ── 极简 CDP 客户端 ────────────────────────────────────────────
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    ws.addEventListener('message', (event) => {
      const msg = JSON.parse(event.data);
      const entry = this.pending.get(msg.id);
      if (!entry) return;
      this.pending.delete(msg.id);
      if (msg.error) entry.reject(new Error(msg.error.message));
      else entry.resolve(msg.result);
    });
  }

  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
}

const targets = await (await fetch(`http://127.0.0.1:${PORT}/json`)).json();
const page = targets.find((t) => t.type === 'page');
if (!page) throw new Error('找不到可用的页面目标');

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.addEventListener('open', resolve, { once: true });
  ws.addEventListener('error', reject, { once: true });
});

const cdp = new CDP(ws);
await cdp.send('Page.enable');
await cdp.send('Runtime.enable');

// ── 辅助 ──────────────────────────────────────────────────────

async function goto(path) {
  await cdp.send('Page.navigate', { url: `${BASE}${path}` });
  // 等 load 事件 + 一小段时间给入场动画跑完。
  // 截图必须等动画结束，否则拍到的是半透明的中间态。
  await sleep(1600);
}

async function evaluate(expression) {
  const result = await cdp.send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
  });
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text ?? '页面脚本异常');
  }
  return result.result.value;
}

async function shoot(name) {
  const { data } = await cdp.send('Page.captureScreenshot', {
    format: 'png',
    captureBeyondViewport: false,
  });
  const file = `${OUT_DIR}/${name}.png`;
  writeFileSync(file, Buffer.from(data, 'base64'));
  console.log(`  ✓ ${name}.png`);
}

// ── 开始截图 ──────────────────────────────────────────────────

console.log('登录页…');
await goto('/login');
await shoot('01-login');

console.log('登录中…');
const loginResult = await evaluate(`
  (async () => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ password: ${JSON.stringify(PASSWORD)} }),
    });
    return res.status;
  })()
`);

if (loginResult !== 200) {
  throw new Error(`登录失败，HTTP ${loginResult}`);
}
console.log('  ✓ 已登录');

const pages = [
  ['/', '02-dashboard'],
  ['/bots', '03-bots'],
  ['/sessions', '04-sessions'],
  ['/rules', '05-rules'],
  ['/audit', '06-audit'],
  ['/settings', '07-settings'],
];

for (const [path, name] of pages) {
  console.log(`${path} …`);
  await goto(path);
  await shoot(name);
}

// 命令面板：单独触发一次 ⌘K
console.log('命令面板…');
await cdp.send('Input.dispatchKeyEvent', {
  type: 'keyDown',
  modifiers: 4, // Ctrl
  key: 'k',
  code: 'KeyK',
  windowsVirtualKeyCode: 75,
});
await sleep(900);
await shoot('08-command-palette');

// 浅色主题
console.log('浅色主题…');
await goto('/');
await evaluate(`document.documentElement.classList.remove('dark'); document.documentElement.classList.add('light'); localStorage.setItem('tgs.theme','light'); true`);
await sleep(700);
await shoot('09-dashboard-light');

// 光标聚光：截图看不出悬停效果，必须真的把鼠标移上去。
// 这是验证「交互细节确实生效」而不是「代码写了但没跑」的唯一办法。
console.log('光标聚光…');
await goto('/');
await cdp.send('Input.dispatchMouseEvent', {
  type: 'mouseMoved',
  x: 420,
  y: 190,
  buttons: 0,
});
await sleep(500);
await shoot('10-card-spotlight');

console.log(`\n全部完成，输出在 ${OUT_DIR}/`);
chrome.kill();
process.exit(0);
