<script setup lang="ts">
/**
 * 环境光晕层。
 *
 * **这是整个磨砂主题能成立的前提。**
 *
 * backdrop-filter 只是把背后的内容模糊掉。如果背后是纯色，模糊之后
 * 还是一块纯色 —— 面板看起来就只是「稍微亮一点的板子」，
 * 与高级感毫无关系。
 *
 * ── 位置是这里最关键的设计决策 ──────────────────────────────
 *
 * 第一版把光斑放在画面的两个对角（左上、右下）。截图显示那完全没用：
 * 磨砂的侧边栏与顶栏所在的位置背后**什么都没有**，于是它们依然是
 * 死板的深色块。
 *
 * 所以光斑必须落在**磨砂表面覆盖的区域**上：
 *   - 一块压在左侧 0~220px 的竖条（侧边栏），
 *   - 一块压在顶部 0~56px 的横条（顶栏），
 *   - 一块在中部偏右，给内容区一点色温变化与纵深。
 *
 * ── 幅度 ────────────────────────────────────────────────
 *
 * 漂移周期 60~90 秒、位移几个百分点。移动大了像屏保，
 * 那与「高级」是相反的方向。
 */
</script>

<template>
  <div class="ambient" aria-hidden="true">
    <!-- 左上：压在侧边栏与顶栏的交汇处。这是最重要的一盏 ——
         侧边栏的磨砂能不能读出来，几乎全看它。 -->
    <div
      class="ambient-orb"
      style="
        width: 780px;
        height: 780px;
        top: -180px;
        left: -160px;
        background: radial-gradient(circle, var(--orb-a), transparent 66%);
        animation: drift-a 84s ease-in-out infinite;
      "
    />

    <!-- 顶部中段：让顶栏的磨砂有连续的、横跨的水平色带，
         而不是只在左端有一团 -->
    <div
      class="ambient-orb"
      style="
        width: 900px;
        height: 480px;
        top: -300px;
        left: 28%;
        background: radial-gradient(circle, var(--orb-b), transparent 68%);
        animation: drift-b 96s ease-in-out infinite;
      "
    />

    <!-- 右下：给画面下缘一点紫罗兰色温，同时避免整体偏冷成「企业蓝」 -->
    <div
      class="ambient-orb"
      style="
        width: 820px;
        height: 820px;
        bottom: -400px;
        right: -240px;
        background: radial-gradient(circle, var(--orb-c), transparent 70%);
        animation: drift-c 78s ease-in-out infinite;
      "
    />

    <!-- 中部偏右：青色点缀。三个以上光斑才有纵深感 ——
         两个容易看成一条对角线，三个以上才会读成「空间」。 -->
    <div
      class="ambient-orb"
      style="
        width: 640px;
        height: 640px;
        top: 34%;
        left: 48%;
        background: radial-gradient(circle, var(--orb-d), transparent 72%);
        opacity: calc(var(--ambient-opacity, 0.5) * 0.55);
        animation: drift-a 108s ease-in-out infinite reverse;
      "
    />
  </div>
</template>
