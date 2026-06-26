// web/lib/iife-audit.test.js —— 静态分析 admin.html
//
// ⚠️ 这个测试就是为 v4.13.0 那天踩的坑写的：
//   - admin.html 用 IIFE 写法 \`const App = (() => { ... return { ... }; })();\`
//   - 加新函数时必须**同时**写 function 定义 + 加进 return 对象
//   - 漏了 return：onclick 一调就 TypeError（生产事故）
//
// 这个测试扫 admin.html：
//   1. 找到所有 App.X 引用（onclick / oninput / onchange / 内部调用 / state.X.X.X）
//   2. 找到 return { ... }; 块里所有暴露的函数名
//   3. 断言：每个被引用的 X 都必须在 return 里
//
// 跑：npm test
//
// v4.14 计划：admin.html 拆 ESM，return 对象消失，这个测试改成"每个 import 都有 export"

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const adminHtmlPath = join(__dirname, '..', '..', 'static', 'admin.html');
const adminHtml = readFileSync(adminHtmlPath, 'utf-8');

// 从 admin.html 提取 IIFE return { ... }; 块
//
// 用 `// expose public API` 注释当锚点找起点（admin.html IIFE 末尾的注释）
// 然后找 `};\n})();` 找终点（IIFE 收尾的标志）
// 这样不会被函数体里其他 `return { ... }` 干扰（v4.13.0 调试踩过这个坑）
function extractReturnBlock(src) {
  // 起点：`// expose public API` 注释后面的 `return {`
  const anchorIdx = src.indexOf('// expose public API');
  if (anchorIdx < 0) {
    throw new Error('admin.html 里找不到 "// expose public API" 注释 —— IIFE 收尾的结构变了？');
  }
  const startMarker = 'return {';
  const startIdx = src.indexOf(startMarker, anchorIdx);
  if (startIdx < 0) {
    throw new Error('"// expose public API" 注释后找不到 "return {"');
  }
  // 终点：`};\n})();` （IIFE 收尾）
  const endMarker = '};' + '\n})();';
  const endIdx = src.indexOf(endMarker, startIdx);
  if (endIdx < 0) {
    throw new Error('找不到 "};\n})();" —— IIFE 收尾结构变了？');
  }
  return src.slice(startIdx + startMarker.length, endIdx);
}

// 从 return 块提取所有暴露的函数名
function extractExposedNames(returnBlock) {
  const names = new Set();
  for (const line of returnBlock.split('\n')) {
    // 去掉 // 行尾注释
    const stripped = line.replace(/\/\/.*$/, '');
    // 按逗号切，每段 trim 后应该是合法标识符
    for (const part of stripped.split(',')) {
      const name = part.trim();
      if (/^[a-zA-Z_$][a-zA-Z0-9_$]*$/.test(name)) {
        names.add(name);
      }
    }
  }
  return names;
}

// 从全文提取所有 App.X 引用
function extractAppReferences(src) {
  const refs = new Set();
  const re = /App\.([a-zA-Z_$][a-zA-Z0-9_$]*)/g;
  let match;
  while ((match = re.exec(src)) !== null) {
    refs.add(match[1]);
  }
  return refs;
}

describe('admin.html IIFE return object audit', () => {
  it('能找到 IIFE return 块', () => {
    const block = extractReturnBlock(adminHtml);
    expect(block.length).toBeGreaterThan(100);
    // 至少应该有一些常见函数名
    expect(block).toContain('init');
    expect(block).toContain('doLogin');
  });

  it('每个 App.X 引用都暴露在 return 对象里', () => {
    const exposed = extractExposedNames(extractReturnBlock(adminHtml));
    const refs = extractAppReferences(adminHtml);

    // 调试用：打印找到的引用数 / 暴露数
    // console.log(`refs: ${refs.size}, exposed: ${exposed.size}`);

    // 找没暴露的（漏挂到 App 的）
    const missing = [...refs].filter(name => !exposed.has(name));
    expect(
      missing,
      `App.X 引用了但 return 对象里没有:\n  ${missing.join(', ')}\n\n` +
        '修复：把这些名字加进 static/admin.html 的 IIFE return { ... } 块里。\n' +
        '参考位置：搜 "expose public API"。',
    ).toEqual([]);
  });

  it('return 块里每个名字都能在 App.X 引用里找到（避免 dead export）', () => {
    // 反向检查：return 里列了但没人调 = 死代码
    // 注意：少数 init / doLogin 等是 init 启动入口（addEventListener 调），允许不在 App.X 里
    const exposed = extractExposedNames(extractReturnBlock(adminHtml));
    const refs = extractAppReferences(adminHtml);

    // 允许"出口方法"：这些是 event listener / 内部触发，不在 App.X 引用里
    //   - init / doLogin / logout / drillDown / refreshCurrent / closeModal / openModal
    //     → DOMContentLoaded / nav click / 顶栏刷新 / modal 关闭按钮
    //   - loadXxx（除 loadAppointments / loadBarbers / loadCustomers / loadEvents /
    //              loadChainWeekly / loadNotifications / loadServices / loadWeekly
    //              这些会被 onclick 调）→ 大部分 loadXxx 是 nav click handler 内部调
    //     不是 onclick 属性，所以没 App.X 引用
    //   - loadAlerts → 内部调
    //   - loadPlatformOverview → 内部调（drilLDown 也走 setView 不走 App.loadPlatformOverview）
    const ALLOWED_NO_REFS = new Set([
      'init', 'doLogin', 'logout', 'drillDown', 'refreshCurrent',
      'closeModal', 'openModal',
      // 大部分 loadXxx 是 nav click handler 内部调（裸函数名，不是 App.X）
      'loadAlerts', 'loadChain', 'loadHandoffs', 'loadLeaves', 'loadMembers',
      'loadPlans', 'loadPlatformOverview', 'loadShop', 'loadShops', 'loadSubscription',
      'loadCards', 'loadSoldCards',  // v4.15 卡管理：tab 切换 + refreshCurrent 内部调
      // v4.16 批量 / 撤销 / 内部 helper（裸名调用）
      'closeApptMenu', 'refreshBatchToolbar', 'pushUndo', 'undoLast',
    ]);

    const deadExports = [...exposed].filter(name => !refs.has(name) && !ALLOWED_NO_REFS.has(name));
    expect(
      deadExports,
      `return 块里列了但没人调（可能是死代码）:\n  ${deadExports.join(', ')}\n\n` +
        '如果是真不需要的，从 return 块里删。\n' +
        '如果是 event listener 触发的，加进 ALLOWED_NO_REFS。',
    ).toEqual([]);
  });

  it('App.init 在 DOMContentLoaded 时被注册', () => {
    // 兜底：保证文件末尾有 App.init 注册（漏了 = admin.html 打开啥也不发生）
    expect(adminHtml).toMatch(/addEventListener\(['"]DOMContentLoaded['"],\s*App\.init\)/);
  });
});
