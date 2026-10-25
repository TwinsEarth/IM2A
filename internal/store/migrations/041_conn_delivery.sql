-- SPDX-License-Identifier: MIT
--
-- B2-b（task-17）内核侧投递持久化：发号表 / 投递日志 / 设备位点。
--
-- 编号说明：B2B-DESIGN.md 起草时写 039，但实际最大编号已是 040_seq_user_id.sql，
-- 按 lead 裁决顺延为 041（前向编号由后续迁移继续使用，不复用 039/040）。
--
-- 幂等纪律（见 B2B-DESIGN §1.3）：只做 CREATE TABLE/INDEX IF NOT EXISTS，
-- 不做 ALTER、不建可能因历史数据失败的 UNIQUE INDEX；迁移每次启动都会重跑。

-- 1) 每用户投递序号分配器（与 im_delivery_log 同一事务使用）。
CREATE TABLE IF NOT EXISTS im_user_delivery_seq (
    user_id    BIGINT PRIMARY KEY,
    last_seq   BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2) 投递日志（B2B-DESIGN §1.2 冻结形状）：内核写、网关不解析；客户端重连按 seq 补发。
--    PRIMARY KEY (user_id, seq) 即补发查询覆盖索引，不再额外建。
CREATE TABLE IF NOT EXISTS im_delivery_log (
    user_id    BIGINT NOT NULL,
    seq        BIGINT NOT NULL,
    msg_type   SMALLINT NOT NULL DEFAULT 1,
    flags      SMALLINT NOT NULL DEFAULT 0,
    payload    BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_delivery_log_expire
    ON im_delivery_log (created_at);

-- 3) 设备级投递位点（B2B-DESIGN §1.1）。
--    device_id 与 devices.id 同源（035_devices.sql），但 devices.user_id 是 TEXT、
--    本表 user_id 用 BIGINT：跨表 join 需显式转换（历史不一致，Wave C 统一时收口）。
--    acked_seq 只增不减：更新语句固定 GREATEST(acked_seq, $new)。
CREATE TABLE IF NOT EXISTS im_device_cursor (
    device_id    TEXT PRIMARY KEY,
    user_id      BIGINT NOT NULL,
    acked_seq    BIGINT NOT NULL DEFAULT 0,
    conn_epoch   BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_device_cursor_user
    ON im_device_cursor (user_id);
