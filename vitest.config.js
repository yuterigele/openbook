// vitest.config.js —— vitest 配置（v4.13.0 JS 测试基建）
//
// 目标：
//   - 跑 web/**/*.test.js
//   - 静态分析测试（iife-audit.test.js）读 static/admin.html 也走这里
//   - 不需要 jsdom（纯函数 + 文件读，Node 环境就够）
//
// 后续如果要给 admin.html 写"渲染"类测试（模拟 DOM + 触发点击），再开 jsdom

import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['web/**/*.test.js'],
    environment: 'node',
    reporters: ['default'],
    // 慢测试提醒（避免意外的 IO 测试拖慢 CI）
    testTimeout: 5000,
  },
});
