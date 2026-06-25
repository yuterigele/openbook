// web/lib/format.js —— 纯函数（v4.13.0 从 static/admin.html IIFE 抽出来便于测）
//
// 为什么抽这些：
//   - 无副作用、不依赖 DOM / 全局状态 / 网络
//   - 是 admin.html 里 bug 最多的小工具（escape、格式化、套餐文案等）
//   - 单元测试成本低、ROI 高
//
// ⚠️ 同步约束：
//   - admin.html IIFE 里**也有一份同样的函数**（避免一次性把 5000 行改成 ESM 砸了线上）
//   - 修改这里的函数**必须同步**改 admin.html 里那份
//   - 未来 v4.14 计划把 admin.html 拆 ESM，到时候 admin.html 改成 import 这里的，重复就消除了
//   - 在那之前，web/lib/iife-audit.test.js 会扫 admin.html 验证"function 定义 + return 暴露"对得上

// esc HTML 特殊字符（< > & " '）
// 跟 admin.html 的 esc() 一致；用于 onclick/oninput 属性内插值
export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  })[c]);
}

// fmtPct 把 0~1 的小数转成 "XX.X%"
export function fmtPct(x) {
  return (x * 100).toFixed(1) + '%';
}

// fmtDate "YYYY-MM-DD" 字符串展示；空 → "—"
export function fmtDate(s) {
  return s ? esc(s) : '—';
}

// fmtTime "HH:MM" 取前 5 字符
export function fmtTime(s) {
  return s ? esc(s.slice(0, 5)) : '—';
}

// pad 数字前补 0 到 2 位
export function pad(n) {
  return String(n).padStart(2, '0');
}

// parseQuery "tag=BLACKLIST&foo=bar" → {tag:'BLACKLIST', foo:'bar'}
// admin.html drillDown / alert 跳转用
export function parseQuery(q) {
  if (!q) return {};
  return q.split('&').reduce((acc, p) => {
    const [k, v] = p.split('=');
    if (k) acc[k] = v || '';
    return acc;
  }, {});
}

// daysLeft 距 expiresAt 还有几天（向下取整；过期返 0）
export function daysLeft(expiresAt) {
  if (!expiresAt) return 0;
  const ms = new Date(expiresAt) - new Date();
  return Math.max(0, Math.ceil(ms / (24 * 3600 * 1000)));
}

// planPriceYuan 分 → "¥XX/月"（enterprise 价 0 → "按需谈"）
export function planPriceYuan(cents) {
  if (cents === 0) return '按需谈';
  const yuan = cents / 100;
  return `¥${yuan.toLocaleString('zh-CN')}/月`;
}

// planPill plan id → 套餐 pill HTML（给 plan pill 加颜色 class）
// 跟 admin.html planPill(plan) 行为一致
export function planPill(plan) {
  const cls = plan === 'flagship' ? 'plan-flagship' : plan === 'pro' ? 'plan-pro' : '';
  const label = plan === 'flagship' ? '旗舰' : plan === 'pro' ? '专业' : plan === 'basic' ? '基础' : (plan || '—');
  return `<span class="plan-pill ${cls}">${esc(label)}</span>`;
}

// planStatusText 给前端友好显示 plan 状态
//   {tone, label} tone: 'danger' | 'warning' | 'success' | 'muted'
// 跟 admin.html planStatusText(p) 行为一致
export function planStatusText(p) {
  if (p.frozen) return { tone: 'danger', label: '已冻结，请续费' };
  if (p.grace_days > 0) return { tone: 'warning', label: `宽限期内，剩 ${p.grace_days} 天` };
  if (p.days_left > 0) return { tone: 'success', label: `还有 ${p.days_left} 天到期` };
  if (p.expires_at) return { tone: 'danger', label: '已到期' };
  return { tone: 'muted', label: '未订阅' };
}

// planFeatureLabel feature id → 中文标签
export function planFeatureLabel(f) {
  return ({
    data_export: '数据导出（CSV）',
    api_access: 'API 接入',
    multi_store: '多店连锁',
    custom_report: '定制报表',
    priority_support: '专属客服',
    sla_guarantee: 'SLA 保障',
  })[f] || f;
}

// statusLabel 预约状态 → 中文
export function statusLabel(s) {
  return ({ active: '待到店', completed: '已完成', noshow: '爽约', cancelled: '已取消' })[s] || s || '—';
}
