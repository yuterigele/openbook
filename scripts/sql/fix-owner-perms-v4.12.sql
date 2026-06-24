-- 紧急 SQL: 修正 production 老店铺 owner perm 矩阵（v4.12 部署后必跑）
--
-- Root cause: v4.10.1 (commit 208279a) 改了 storage 代码收紧 owner 矩阵，
--   v4.12 (commit 661807e) 加 view:plan perm，但两次都没让 production DB 同步。
--   老店铺 owner 矩阵还停在 v4.7 时代的 AllPermissions（含 view:chain_dashboard /
--   view:subscription / manage:subscription —— 这些 v4.10.1 收紧后 owner 不该有）。
--
-- 影响：meHandler 走真 DB 矩阵 → 前端拿到的 permissions 含这 3 个 → nav 显示跨店看板 /
--   跨店周报 / 订阅菜单（v4.10.1 设计是 owner 都看不到，归 platform_admin only）。
--   同时 view:plan（v4.12 新加）owner 没有 → 套餐与升级菜单不显示。
--
-- 跑法（任选一种）:
--   A) 命令行（推荐）:
--      mysql chatwitheino < /tmp/fix-owner-perms-v4.12.sql
--   B) 图形客户端（Navicat / DataGrip 等）打开 SQL 窗口执行
--   C) 用 admin-tool reconcile（v4.12 推荐路径，2026-06-25 之前会加新子命令 perms migrate）
--
-- 跑完后 F12 → /api/admin/me 应能看到 owner 的 permissions:
--   应该有 view:plan / view:weekly_report
--   不该有 view:chain_dashboard / view:subscription / manage:subscription

-- 1) 删 v4.10.1 收紧的 3 个 perm（owner 不该有）
DELETE FROM role_permissions
WHERE role = 'owner'
  AND permission IN (
    'view:chain_dashboard',  -- 跨店看板（v4.10.1 收紧：归 platform_admin）
    'view:subscription',      -- 订阅详情（v4.10.1 收紧：归 platform_admin）
    'manage:subscription'     -- 续费（v4.10.1 收紧：归 platform_admin）
  );

-- 2) 加 v4.12 新加的 view:plan（owner 该有，reconcile 也会自动补，这里保险起见显式加）
INSERT OR IGNORE INTO role_permissions (role, permission) VALUES
  ('owner', 'view:plan');

-- 3) 验证：列出 owner 现在的 perm（应该有 19 个，跟 storage.DefaultRolePermissions[RoleOwner] 对齐）
SELECT role, permission
FROM role_permissions
WHERE role = 'owner'
ORDER BY permission;
-- 期望看到 19 个 perm，包括 view:dashboard / view:appointments / edit:appointments / view:customers /
-- edit:customers / view:handoffs / resolve:handoff / view:barbers / edit:barbers / create:barber_leave /
-- view:events / view:weekly_report / edit:shop / view:services / edit:services / manage:members /
-- change:own_password / view:notifications / retry:notifications / view:plan
-- 不该看到：view:chain_dashboard / view:subscription / manage:subscription

-- 4) 验证 staff perm（应该 14 个，含 view:plan 不出现）
SELECT role, permission
FROM role_permissions
WHERE role = 'staff'
ORDER BY permission;
-- 期望看到 view:dashboard / view:appointments / edit:appointments / view:customers /
-- edit:customers / view:handoffs / resolve:handoff / view:barbers / create:barber_leave /
-- view:events / view:services / view:notifications / retry:notifications / change:own_password
-- 不该看到：view:plan / view:chain_dashboard / view:subscription / view:weekly_report
