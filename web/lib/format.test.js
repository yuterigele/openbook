// web/lib/format.test.js —— format.js 的单元测试
//
// 跑：npm test

import { describe, it, expect } from 'vitest';
import {
  esc, fmtPct, fmtDate, fmtTime, pad, parseQuery,
  daysLeft, planPriceYuan, planPill, planStatusText, planFeatureLabel, statusLabel,
} from './format.js';

describe('esc', () => {
  it('escapes all 5 HTML special chars', () => {
    expect(esc('<a href="x" onclick=\'y\'>&</a>'))
      .toBe('&lt;a href=&quot;x&quot; onclick=&#39;y&#39;&gt;&amp;&lt;/a&gt;');
  });
  it('handles null / undefined / number', () => {
    expect(esc(null)).toBe('');
    expect(esc(undefined)).toBe('');
    expect(esc(0)).toBe('0');
  });
  it('preserves non-special chars', () => {
    expect(esc('hello world 123')).toBe('hello world 123');
  });
  it('escapes single quote (今天踩坑的字符)', () => {
    // 之前 admin.html 把 esc() 出来的字符串拼进 onclick 属性
    // 浏览器解析 HTML 属性时把 &#39; 解码回 ' → JS 字符串会断
    // 正确做法：js 字符串用 JSON.stringify 转义，不要用 esc
    // 但 esc 函数本身的正确性还是测一下
    expect(esc("Tony's shop")).toBe('Tony&#39;s shop');
  });
});

describe('fmtPct', () => {
  it('0.5 → 50.0%', () => {
    expect(fmtPct(0.5)).toBe('50.0%');
  });
  it('0.155 → 15.5%', () => {
    expect(fmtPct(0.155)).toBe('15.5%');
  });
  it('0 → 0.0%', () => {
    expect(fmtPct(0)).toBe('0.0%');
  });
  it('1.234 → 123.4%', () => {
    expect(fmtPct(1.234)).toBe('123.4%');
  });
});

describe('fmtDate', () => {
  it('passes through date string', () => {
    expect(fmtDate('2026-06-25')).toBe('2026-06-25');
  });
  it('empty / null → "—"', () => {
    expect(fmtDate('')).toBe('—');
    expect(fmtDate(null)).toBe('—');
    expect(fmtDate(undefined)).toBe('—');
  });
  it('escapes special chars in date string', () => {
    expect(fmtDate('<script>')).toBe('&lt;script&gt;');
  });
});

describe('fmtTime', () => {
  it('takes first 5 chars of HH:MM:SS', () => {
    expect(fmtTime('14:30:00')).toBe('14:30');
  });
  it('preserves HH:MM', () => {
    expect(fmtTime('09:05')).toBe('09:05');
  });
  it('empty → "—"', () => {
    expect(fmtTime('')).toBe('—');
    expect(fmtTime(null)).toBe('—');
  });
});

describe('pad', () => {
  it('zero-pads 1-digit numbers', () => {
    expect(pad(0)).toBe('00');
    expect(pad(5)).toBe('05');
    expect(pad(9)).toBe('09');
  });
  it('preserves 2-digit numbers', () => {
    expect(pad(10)).toBe('10');
    expect(pad(23)).toBe('23');
  });
});

describe('parseQuery', () => {
  it('parses single k=v', () => {
    expect(parseQuery('tag=BLACKLIST')).toEqual({ tag: 'BLACKLIST' });
  });
  it('parses multi k=v', () => {
    expect(parseQuery('tag=VIP&status=active&limit=20'))
      .toEqual({ tag: 'VIP', status: 'active', limit: '20' });
  });
  it('handles key without value', () => {
    expect(parseQuery('flag')).toEqual({ flag: '' });
  });
  it('empty / null → {}', () => {
    expect(parseQuery('')).toEqual({});
    expect(parseQuery(null)).toEqual({});
  });
});

describe('daysLeft', () => {
  it('future date → positive days', () => {
    const future = new Date(Date.now() + 5 * 24 * 3600 * 1000).toISOString();
    expect(daysLeft(future)).toBeGreaterThanOrEqual(4);
    expect(daysLeft(future)).toBeLessThanOrEqual(6);
  });
  it('past date → 0 (clamped)', () => {
    const past = new Date(Date.now() - 5 * 24 * 3600 * 1000).toISOString();
    expect(daysLeft(past)).toBe(0);
  });
  it('null / empty → 0', () => {
    expect(daysLeft(null)).toBe(0);
    expect(daysLeft('')).toBe(0);
  });
});

describe('planPriceYuan', () => {
  it('basic 9900 cents → "¥99/月"', () => {
    expect(planPriceYuan(9900)).toBe('¥99/月');
  });
  it('flagship 99900 cents → "¥999/月"', () => {
    expect(planPriceYuan(99900)).toBe('¥999/月');
  });
  it('0 → "按需谈" (enterprise)', () => {
    expect(planPriceYuan(0)).toBe('按需谈');
  });
  it('formats with thousands separator', () => {
    expect(planPriceYuan(199900)).toBe('¥1,999/月');
  });
});

describe('planPill', () => {
  it('flagship → plan-flagship class + 旗舰 label', () => {
    expect(planPill('flagship')).toContain('plan-flagship');
    expect(planPill('flagship')).toContain('旗舰');
  });
  it('pro → plan-pro class + 专业 label', () => {
    expect(planPill('pro')).toContain('plan-pro');
    expect(planPill('pro')).toContain('专业');
  });
  it('basic → no class + 基础 label', () => {
    expect(planPill('basic')).toContain('基础');
    expect(planPill('basic')).not.toContain('plan-pro');
  });
  it('unknown plan → shows id as label', () => {
    expect(planPill('unicorn')).toContain('unicorn');
  });
  it('null / empty → "—"', () => {
    expect(planPill(null)).toContain('—');
    expect(planPill('')).toContain('—');
  });
});

describe('planStatusText', () => {
  it('frozen → danger tone', () => {
    const s = planStatusText({ frozen: true, days_left: 0 });
    expect(s.tone).toBe('danger');
    expect(s.label).toContain('冻结');
  });
  it('in grace period → warning tone', () => {
    const s = planStatusText({ frozen: false, grace_days: 5, days_left: 0 });
    expect(s.tone).toBe('warning');
    expect(s.label).toContain('5 天');
  });
  it('active with days_left > 0 → success', () => {
    const s = planStatusText({ frozen: false, grace_days: 0, days_left: 30 });
    expect(s.tone).toBe('success');
    expect(s.label).toContain('30 天');
  });
  it('expired (days_left=0 + no grace) → danger', () => {
    const s = planStatusText({ frozen: false, grace_days: 0, days_left: 0, expires_at: '2026-01-01' });
    expect(s.tone).toBe('danger');
    expect(s.label).toContain('已到期');
  });
  it('no subscription → muted', () => {
    const s = planStatusText({ frozen: false, grace_days: 0, days_left: 0, expires_at: null });
    expect(s.tone).toBe('muted');
    expect(s.label).toContain('未订阅');
  });
});

describe('planFeatureLabel', () => {
  it('translates known features to Chinese', () => {
    expect(planFeatureLabel('data_export')).toBe('数据导出（CSV）');
    expect(planFeatureLabel('api_access')).toBe('API 接入');
    expect(planFeatureLabel('multi_store')).toBe('多店连锁');
  });
  it('passes through unknown feature', () => {
    expect(planFeatureLabel('new_feature')).toBe('new_feature');
  });
});

describe('statusLabel', () => {
  it('translates appointment status', () => {
    expect(statusLabel('active')).toBe('待到店');
    expect(statusLabel('completed')).toBe('已完成');
    expect(statusLabel('noshow')).toBe('爽约');
    expect(statusLabel('cancelled')).toBe('已取消');
  });
  it('passes through unknown', () => {
    expect(statusLabel('foo')).toBe('foo');
  });
  it('null / empty → "—"', () => {
    expect(statusLabel(null)).toBe('—');
    expect(statusLabel('')).toBe('—');
  });
});
