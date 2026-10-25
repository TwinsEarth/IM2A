-- v1.5 收口：老表 payment_ledger(v0.9 种子) -> paid_message_ledger
-- 映射规则（确定性、可追溯）：
--   message_id varchar  -> md5('legacy:v0.9:'||msg)::uuid；approval_card_id 保留原串
--   sender/receiver 用户名 -> 种子数字ID（不与真实用户冲突）
--     stranger1=1001, stranger2=1005, spammer=1004, alice=1002, bob=1003
--   mode: send->1(发送即扣), pay_open->2(回复才扣)
--   status: charged->1(charged_at=created_at), refunded->2(refunded_at=created_at)
BEGIN;

INSERT INTO paid_message_ledger
  (message_id, sender_id, receiver_id, billing_mode, amount_cents,
   status, charged_at, refunded_at, refund_reason, approval_card_id, created_at)
SELECT
  md5('legacy:v0.9:' || p.message_id)::uuid,
  CASE p.sender_id
    WHEN 'stranger1' THEN 1001 WHEN 'stranger2' THEN 1005
    WHEN 'spammer' THEN 1004  WHEN 'alice'    THEN 1002 WHEN 'bob' THEN 1003
  END,
  CASE p.receiver_id
    WHEN 'stranger1' THEN 1001 WHEN 'stranger2' THEN 1005
    WHEN 'spammer' THEN 1004  WHEN 'alice'    THEN 1002 WHEN 'bob' THEN 1003
  END,
  CASE WHEN p.mode = 'pay_open' THEN 2 ELSE 1 END,
  p.amount_cents,
  CASE WHEN p.status = 'refunded' THEN 2 WHEN p.status = 'charged' THEN 1 ELSE 0 END,
  CASE WHEN p.status = 'charged'  THEN p.created_at END,
  CASE WHEN p.status = 'refunded' THEN p.created_at END,
  CASE WHEN p.status = 'refunded' THEN 'legacy v0.9 migrated refunded' END,
  'legacy:' || p.message_id,
  p.created_at
FROM payment_ledger p
ON CONFLICT (message_id) DO NOTHING;

-- 老表只读归档
ALTER TABLE payment_ledger RENAME TO payment_ledger_legacy_v09;

COMMIT;
