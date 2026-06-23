-- scripts/sql/cleanup_no_openid.sql
--
-- 清理"没有 wechat_open_id 的顾客"及其关联数据（v4.9.3）
--
-- ⚠️ 危险操作！删之前请务必先备份！
--
-- 背景：
--   - 老版本 backfill 时建的顾客档案没 wecom ID（v4.8 之前）
--   - v4.9.3 之后这些顾客无法接收 reminder / leave notify cron 消息
--   - 用户决策：直接清掉这些"孤儿"顾客 + 它们的关联数据
--
-- 匹配规则（v4.9.3）：
--   - 只看 wechat_open_id IS NULL
--   - external_user_id 不管（可能某些场景下是有效的）
--
-- 清理范围（外键依赖分析）：
--   1. customers                       按 wechat_open_id IS NULL 删
--   2. appointments.customer_id        关联到 customers.id，级联删
--   3. event_logs.customer_id          关联到 customers.id，级联删
--   4. reminder_logs                   通过 appointment_id 间接，级联删
--
-- 保留（不删）：
--   - barber_leaves: 不存 customer_id，按 barber+time 范围影响预约；
--     清掉预约后逻辑上"受影响预约数"会变，但 leave 行本身保留
--   - subscriptions: 不关联 customer
--   - services / role_permissions / shop_admins: 完全无关
--
-- 用法（务必按顺序）：
--   mysql -uUSER -p DBNAME < cleanup_no_openid.sql --  (dry-run 阶段，看数量)
--   确认数量 OK 后，编辑本文件 uncomment DELETE 段，再跑一次真删

USE chatwitheino;  -- 改成你的库名

-- ============================================================
-- 阶段 1：备份（强烈建议手动执行，不在本脚本里）
-- ============================================================
-- mysqldump -uUSER -p chatwitheino \
--     customers appointments event_logs reminder_logs \
--     > backup_pre_cleanup_20260623.sql

-- ============================================================
-- 阶段 2：DRY RUN —— 看会删多少
-- ============================================================

SELECT '=== 当前没 wechat_open_id 的顾客数 ===' AS info;
SELECT COUNT(*) AS 待删顾客数
FROM customers
WHERE wechat_open_id IS NULL;

SELECT '=== 这些顾客关联的预约数 ===' AS info;
SELECT COUNT(*) AS 待删预约数
FROM appointments
WHERE customer_id IN (
    SELECT id FROM customers WHERE wechat_open_id IS NULL
);

SELECT '=== 这些顾客关联的事件数 ===' AS info;
SELECT COUNT(*) AS 待删事件数
FROM event_logs
WHERE customer_id IN (
    SELECT id FROM customers WHERE wechat_open_id IS NULL
);

SELECT '=== 通过预约关联的提醒日志数 ===' AS info;
SELECT COUNT(*) AS 待删提醒数
FROM reminder_logs
WHERE appointment_id IN (
    SELECT a.id FROM appointments a
    JOIN customers c ON c.id = a.customer_id
    WHERE c.wechat_open_id IS NULL
);

SELECT '=== 待删顾客清单（前 20 条） ===' AS info;
SELECT id, name, phone, created_at
FROM customers
WHERE wechat_open_id IS NULL
ORDER BY created_at DESC
LIMIT 20;

-- ⚠️ 看完上面数量，确认无误后，把下面这段 uncomment 再跑：

/*
-- ============================================================
-- 阶段 3：真删（按依赖顺序：先删子表，再删主表）
-- ============================================================

START TRANSACTION;

-- 3.1) reminder_logs（先删最深层的）
DELETE FROM reminder_logs
WHERE appointment_id IN (
    SELECT a.id FROM appointments a
    JOIN customers c ON c.id = a.customer_id
    WHERE c.wechat_open_id IS NULL
);

-- 3.2) event_logs
DELETE FROM event_logs
WHERE customer_id IN (
    SELECT id FROM customers WHERE wechat_open_id IS NULL
);

-- 3.3) appointments
DELETE FROM appointments
WHERE customer_id IN (
    SELECT id FROM customers WHERE wechat_open_id IS NULL
);

-- 3.4) customers（主表）
DELETE FROM customers
WHERE wechat_open_id IS NULL;

COMMIT;

SELECT '=== 清理完成 ===' AS result;
SELECT '剩余没 wechat_open_id 的顾客数（应该 = 0）' AS check_label, COUNT(*) AS num
FROM customers
WHERE wechat_open_id IS NULL;
*/

-- ============================================================
-- 阶段 4（可选）：重新跑 BackfillMissingCustomers 兜底
-- ============================================================
-- 如果未来又有新的"老 appointment 漏建顾客"的情况，go run ./cmd/migrate 会自愈
-- （已在 InitDB 里挂了 BackfillMissingCustomers，每次启动跑一次）
-- 这里不需要手动做。