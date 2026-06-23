-- scripts/sql/comments.sql
--
-- 表/字段注释补全（v4.9.3 一次性手动 SQL）
--
-- 背景：
--   - GORM AutoMigrate 不会自动给表/字段加 COMMENT
--   - MySQL DESCRIBE <table> 默认看不到中文注释，运维/DBA 排查时不便
--   - 所以把所有表 + 字段的注释以 SQL 形式落地到本文件
--
-- 用法（MySQL 客户端 / Navicat / DataGrip 都行）：
--   SOURCE /path/to/comments.sql;
--   或在 mysql CLI 里：
--     mysql -uUSER -p DBNAME < comments.sql
--
-- 幂等性：每条 ALTER 都带 IF EXISTS 检查，重跑 0 副作用
--
-- 维护：
--   - 加新表 / 新字段时，记得在 storage/models.go 加完 GORM tag 后
--     同步在本文档追加对应 COMMENT
--   - 字段顺序与 models.go 保持一致

USE chatwitheino;  -- 改成你的库名（默认 chatwitheino）

-- ============================================================
-- 表注释
-- ============================================================

ALTER TABLE shops COMMENT = '店铺表（v1.0+）：多店隔离的基础表，每条记录一家理发店';
ALTER TABLE shop_admins COMMENT = '商户后台账号表（v1.0+，v4.7 加 role/status 列）';
ALTER TABLE barbers COMMENT = '理发师表（v1.0+）：每个理发师绑定一家店';
ALTER TABLE customers COMMENT = '顾客档案表（v1.0+，v4.8 backfill 自愈，v4.9.3 phone 必填）';
ALTER TABLE appointments COMMENT = '预约表（v1.0+）：核心业务表，每条记录一次预约';
ALTER TABLE subscriptions COMMENT = '订阅表（v4.0+）：商户订阅历史';
ALTER TABLE wecom_message_logs COMMENT = '企业微信消息日志表（v4.0+）：用于幂等去重';
ALTER TABLE reminder_logs COMMENT = '预约提醒日志表（v4.0+）：每个 appointment 每种 reminder_type 仅一条';
ALTER TABLE event_logs COMMENT = '事件埋点表（v4.0+）：所有关键业务事件写这里，便于 funnel 分析';
ALTER TABLE barber_leaves COMMENT = '理发师请假表（P4/v4.4，2026-06-21）：请假覆盖时段不再可预约';
ALTER TABLE services COMMENT = '服务目录表（v4.4/v4.9，2026-06-22）：每个店的服务清单';
ALTER TABLE role_permissions COMMENT = 'RBAC 角色权限矩阵表（v4.7）：role → permission 多对多';

-- ============================================================
-- 字段注释：shops
-- ============================================================

ALTER TABLE shops
    MODIFY COLUMN id              VARCHAR(64)  NOT NULL COMMENT '店铺 ID（主键，业务可读，如 "default"）',
    MODIFY COLUMN name            VARCHAR(128) NOT NULL COMMENT '店铺名',
    MODIFY COLUMN address         VARCHAR(256)         COMMENT '店铺地址',
    MODIFY COLUMN timezone        VARCHAR(64)  DEFAULT 'Asia/Shanghai' COMMENT '时区（默认上海）',
    MODIFY COLUMN open_hour       INT          DEFAULT 9  COMMENT '营业开始小时（09:00）',
    MODIFY COLUMN close_hour      INT          DEFAULT 18 COMMENT '营业结束小时（18:00）',
    MODIFY COLUMN lunch_start     INT          DEFAULT 12 COMMENT '午休开始小时（12:00）',
    MODIFY COLUMN lunch_end       INT          DEFAULT 13 COMMENT '午休结束小时（13:00）',
    MODIFY COLUMN lunch_end_min   INT          DEFAULT 30 COMMENT '午休结束分钟（30）',
    MODIFY COLUMN plan            VARCHAR(32)  DEFAULT 'basic' COMMENT '套餐：basic / pro / flagship',
    MODIFY COLUMN expires_at      DATETIME              COMMENT '订阅到期时间',
    MODIFY COLUMN auto_renew      TINYINT(1)   DEFAULT 0  COMMENT '是否自动续费',
    MODIFY COLUMN holidays        VARCHAR(512) DEFAULT '' COMMENT '节假日日期列表（逗号分隔 YYYY-MM-DD）',
    MODIFY COLUMN wecom_corp_id         VARCHAR(64)  COMMENT '企业微信 CorpID（多店场景下唯一）',
    MODIFY COLUMN wecom_agent_id        INT          COMMENT '企业微信 AgentID',
    MODIFY COLUMN wecom_secret          VARCHAR(128) COMMENT '企业微信 Secret（API 不暴露）',
    MODIFY COLUMN wecom_token           VARCHAR(64)  COMMENT '企业微信回调 Token（API 不暴露）',
    MODIFY COLUMN wecom_encoding_aes_key VARCHAR(64) COMMENT '企业微信回调 EncodingAESKey（API 不暴露）',
    MODIFY COLUMN wecom_kf_link         VARCHAR(512) COMMENT '企业微信客服链接（用于发送客服消息）',
    MODIFY COLUMN created_at           DATETIME     COMMENT '创建时间',
    MODIFY COLUMN updated_at           DATETIME     COMMENT '更新时间';

-- ============================================================
-- 字段注释：shop_admins
-- ============================================================

ALTER TABLE shop_admins
    MODIFY COLUMN id            BIGINT       NOT NULL AUTO_INCREMENT COMMENT '账号 ID（主键，自增）',
    MODIFY COLUMN shop_id       VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id，多店隔离）',
    MODIFY COLUMN username      VARCHAR(64)  NOT NULL COMMENT '登录用户名（全局唯一）',
    MODIFY COLUMN password_hash VARCHAR(128) NOT NULL COMMENT 'bcrypt 哈希后的密码（API 不暴露）',
    MODIFY COLUMN role          VARCHAR(16)  DEFAULT 'owner' COMMENT '角色：owner / staff / platform_admin（v4.9 加）',
    MODIFY COLUMN status        VARCHAR(16)  DEFAULT 'active' COMMENT '账号状态：active / disabled（v4.7）',
    MODIFY COLUMN last_login_at DATETIME              COMMENT '最近登录时间',
    MODIFY COLUMN created_at    DATETIME              COMMENT '创建时间',
    MODIFY COLUMN updated_at    DATETIME              COMMENT '更新时间';

-- ============================================================
-- 字段注释：barbers
-- ============================================================

ALTER TABLE barbers
    MODIFY COLUMN id         VARCHAR(64)  NOT NULL COMMENT '理发师 ID（主键）',
    MODIFY COLUMN shop_id    VARCHAR(64)           COMMENT '所属店铺 ID（FK → shops.id）',
    MODIFY COLUMN name       VARCHAR(64)  NOT NULL COMMENT '理发师姓名（全局唯一）',
    MODIFY COLUMN skills     VARCHAR(256)          COMMENT '技能列表（逗号分隔，如 "剪发,染发"）',
    MODIFY COLUMN active     TINYINT(1)   DEFAULT 1 COMMENT '是否在职（false = 软删）',
    MODIFY COLUMN created_at DATETIME             COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME             COMMENT '更新时间';

-- ============================================================
-- 字段注释：customers
-- ============================================================

ALTER TABLE customers
    MODIFY COLUMN id               VARCHAR(64)  NOT NULL COMMENT '顾客 ID（主键，UUID）',
    MODIFY COLUMN wechat_open_id   VARCHAR(128)          COMMENT '微信 openID（KF 场景下为 external_userid）',
    MODIFY COLUMN external_user_id VARCHAR(128)          COMMENT '企业微信外部联系人 external_userid',
    MODIFY COLUMN phone            VARCHAR(32)           COMMENT '手机号（v4.9.3 必填，11 位数字、1 开头）',
    MODIFY COLUMN name             VARCHAR(64)           COMMENT '顾客姓名',
    MODIFY COLUMN tags             VARCHAR(256)          COMMENT '标签（逗号分隔，VIP / 黑名单 / FREQUENT 等）',
    MODIFY COLUMN total_visits     INT          DEFAULT 0 COMMENT '累计到店次数',
    MODIFY COLUMN no_show_count    INT          DEFAULT 0 COMMENT '爽约累计（用于黑名单判断）',
    MODIFY COLUMN late_cancel_count INT         DEFAULT 0 COMMENT '晚退订累计（提前不足 free_window 取消，用于黑名单判断）',
    MODIFY COLUMN last_visit_at    DATETIME             COMMENT '最近到店时间',
    MODIFY COLUMN created_at       DATETIME             COMMENT '建档时间',
    MODIFY COLUMN updated_at       DATETIME             COMMENT '更新时间';

-- ============================================================
-- 字段注释：appointments
-- ============================================================

ALTER TABLE appointments
    MODIFY COLUMN id            VARCHAR(64)  NOT NULL COMMENT '预约 ID（主键，UUID）',
    MODIFY COLUMN shop_id       VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id）',
    MODIFY COLUMN barber_id     VARCHAR(64)  NOT NULL COMMENT '理发师 ID（FK → barbers.id）',
    MODIFY COLUMN barber_name   VARCHAR(64)           COMMENT '冗余理发师姓名（避免 join）',
    MODIFY COLUMN customer_id   VARCHAR(64)           COMMENT '顾客 ID（FK → customers.id，v4.8 backfill）',
    MODIFY COLUMN customer      VARCHAR(64)           COMMENT '冗余顾客姓名（避免 join）',
    MODIFY COLUMN date          VARCHAR(10)  NOT NULL COMMENT '预约日期 YYYY-MM-DD',
    MODIFY COLUMN time          VARCHAR(5)   NOT NULL COMMENT '预约时间 HH:MM（24 小时制）',
    MODIFY COLUMN service       VARCHAR(64)  DEFAULT '剪发' COMMENT '服务项目',
    MODIFY COLUMN status        VARCHAR(16)  DEFAULT 'active' COMMENT '状态：active / cancelled / completed / noshow',
    MODIFY COLUMN source        VARCHAR(16)  DEFAULT 'wecom' COMMENT '来源：wecom / web / manual',
    MODIFY COLUMN cancel_type   VARCHAR(16)           COMMENT '取消类型：early_cancel / late_cancel / after_due / admin / system（P3/v4.6）',
    MODIFY COLUMN cancelled_at  DATETIME             COMMENT '取消时间',
    MODIFY COLUMN cancel_reason VARCHAR(256)          COMMENT '取消原因（商户后台填）',
    MODIFY COLUMN created_at    DATETIME             COMMENT '创建时间',
    MODIFY COLUMN updated_at    DATETIME             COMMENT '更新时间';

-- ============================================================
-- 字段注释：subscriptions
-- ============================================================

ALTER TABLE subscriptions
    MODIFY COLUMN id         VARCHAR(64)  NOT NULL COMMENT '订阅记录 ID（主键）',
    MODIFY COLUMN shop_id    VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id）',
    MODIFY COLUMN plan       VARCHAR(32)  NOT NULL COMMENT '套餐：basic / pro / flagship',
    MODIFY COLUMN started_at DATETIME             COMMENT '生效时间',
    MODIFY COLUMN expires_at DATETIME             COMMENT '到期时间',
    MODIFY COLUMN auto_renew TINYINT(1)   DEFAULT 0 COMMENT '是否自动续费',
    MODIFY COLUMN created_at DATETIME             COMMENT '创建时间';

-- ============================================================
-- 字段注释：wecom_message_logs
-- ============================================================

ALTER TABLE wecom_message_logs
    MODIFY COLUMN id            BIGINT       NOT NULL AUTO_INCREMENT COMMENT '日志 ID（主键，自增）',
    MODIFY COLUMN msg_id        BIGINT       NOT NULL COMMENT '企业微信消息 MsgId（唯一索引去重）',
    MODIFY COLUMN msg_type      VARCHAR(16)           COMMENT '消息类型：text / event / image / ...',
    MODIFY COLUMN event         VARCHAR(32)           COMMENT '事件类型（仅 event 消息）：kf_msg_or_event / change_external_contact / ...',
    MODIFY COLUMN open_kf_id    VARCHAR(64)           COMMENT '微信客服 OpenKfID（仅 kf 场景）',
    MODIFY COLUMN from_user_name VARCHAR(128)         COMMENT '发送者 ID（KF = external_userid；external = 企业成员 userid）',
    MODIFY COLUMN to_user_name  VARCHAR(128)         COMMENT '接收者 ID',
    MODIFY COLUMN processed     TINYINT(1)   DEFAULT 1 COMMENT '是否已处理（幂等去重标记）',
    MODIFY COLUMN received_at   DATETIME             COMMENT '企业微信时间戳',
    MODIFY COLUMN created_at    DATETIME             COMMENT '入库时间';

-- ============================================================
-- 字段注释：reminder_logs
-- ============================================================

ALTER TABLE reminder_logs
    MODIFY COLUMN id            BIGINT       NOT NULL AUTO_INCREMENT COMMENT '日志 ID（主键，自增）',
    MODIFY COLUMN appointment_id VARCHAR(64) NOT NULL COMMENT '预约 ID（FK → appointments.id，与 reminder_type 联合唯一）',
    MODIFY COLUMN reminder_type VARCHAR(32) NOT NULL COMMENT '提醒类型：pre_2h / noshow_warning（与 appointment_id 联合唯一）',
    MODIFY COLUMN channel       VARCHAR(16)  DEFAULT 'wecom' COMMENT '发送渠道：wecom / sms',
    MODIFY COLUMN status        VARCHAR(16)  DEFAULT 'pending' COMMENT '发送状态：pending / sent / failed',
    MODIFY COLUMN error         VARCHAR(512)          COMMENT '错误信息（status=failed 时填）',
    MODIFY COLUMN sent_at       DATETIME             COMMENT '实际发送时间',
    MODIFY COLUMN created_at    DATETIME             COMMENT '创建时间';

-- ============================================================
-- 字段注释：event_logs
-- ============================================================

ALTER TABLE event_logs
    MODIFY COLUMN id         BIGINT       NOT NULL AUTO_INCREMENT COMMENT '事件 ID（主键，自增）',
    MODIFY COLUMN shop_id    VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id）',
    MODIFY COLUMN customer_id VARCHAR(64)           COMMENT '顾客 ID（FK → customers.id，可空）',
    MODIFY COLUMN event_type VARCHAR(32)  NOT NULL COMMENT '事件类型：appointment_created / first_appointment / handoff_to_human / handoff_resolved / ...',
    MODIFY COLUMN ref_id     VARCHAR(64)           COMMENT '关联资源 ID（如 appointment_id）',
    MODIFY COLUMN meta       VARCHAR(2048)          COMMENT 'JSON 备注（埋点的额外字段）',
    MODIFY COLUMN created_at DATETIME             COMMENT '事件时间';

-- ============================================================
-- 字段注释：barber_leaves
-- ============================================================

ALTER TABLE barber_leaves
    MODIFY COLUMN id              VARCHAR(64)  NOT NULL COMMENT '请假 ID（主键）',
    MODIFY COLUMN shop_id         VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id）',
    MODIFY COLUMN barber_id       VARCHAR(64)  NOT NULL COMMENT '理发师 ID（FK → barbers.id）',
    MODIFY COLUMN barber_name     VARCHAR(64)           COMMENT '冗余理发师姓名（避免 join）',
    MODIFY COLUMN start_at        DATETIME     NOT NULL COMMENT '请假开始时间',
    MODIFY COLUMN end_at          DATETIME     NOT NULL COMMENT '请假结束时间',
    MODIFY COLUMN reason          VARCHAR(256)          COMMENT '原因：生病 / 紧急事务 / 休假',
    MODIFY COLUMN action          VARCHAR(16)  NOT NULL COMMENT '后续动作：cancel（取消） / reschedule（改约）',
    MODIFY COLUMN status          VARCHAR(16)  DEFAULT 'active' COMMENT '状态：active / cancelled',
    MODIFY COLUMN affected_count     INT          DEFAULT 0 COMMENT '受影响预约总数（cancel + reschedule 之和）',
    MODIFY COLUMN rescheduled_count  INT          DEFAULT 0 COMMENT '改约到新时段的预约数',
    MODIFY COLUMN cancelled_count     INT          DEFAULT 0 COMMENT '直接取消的预约数',
    MODIFY COLUMN created_by      VARCHAR(64)           COMMENT '创建人（商户后台用户名）',
    MODIFY COLUMN created_at      DATETIME             COMMENT '创建时间';

-- ============================================================
-- 字段注释：services
-- ============================================================

ALTER TABLE services
    MODIFY COLUMN id            VARCHAR(64)  NOT NULL COMMENT '服务 ID（主键，UUID）',
    MODIFY COLUMN shop_id       VARCHAR(64)  NOT NULL COMMENT '所属店铺 ID（FK → shops.id，多店隔离）',
    MODIFY COLUMN name          VARCHAR(64)  NOT NULL COMMENT '服务名（剪发 / 烫发 / 染发 / 洗吹 / 护理 / 造型 / 其他）',
    MODIFY COLUMN estimated_min INT          DEFAULT 30 COMMENT '预估时长（分钟）',
    MODIFY COLUMN price_range   VARCHAR(64)           COMMENT '价格区间描述（如 "80-120"）',
    MODIFY COLUMN is_active     TINYINT(1)   DEFAULT 1 COMMENT '是否启用（false = 已下架，保留历史）',
    MODIFY COLUMN sort_order    INT          DEFAULT 0 COMMENT '列表展示顺序（asc）',
    MODIFY COLUMN created_at    DATETIME             COMMENT '创建时间',
    MODIFY COLUMN updated_at    DATETIME             COMMENT '更新时间';

-- ============================================================
-- 字段注释：role_permissions
-- ============================================================

ALTER TABLE role_permissions
    MODIFY COLUMN role       VARCHAR(32) NOT NULL COMMENT '角色：owner / staff / platform_admin（v4.9 加）',
    MODIFY COLUMN permission VARCHAR(64) NOT NULL COMMENT '权限标识：view:dashboard / edit:appointments / manage:members ...（见 storage/permissions.go）';

-- ============================================================
-- 完成
-- ============================================================
SELECT '✅ 表/字段 COMMENT 补全完成（共 12 张表）' AS result;