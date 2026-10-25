# IM2A 数据脱敏（v1.8.5）

> 出口（egress）数据保护：手机号 / 身份证 / 银行卡 / 邮箱 / 姓名 / 地址 / 金额，按"角色白名单"脱敏；自由文本内嵌敏感串自动扫描。
> 原则：**默认拒绝、最小暴露**；非法角色一律回落 `other`（全脱敏）。

## 1. 角色与可见性

| 字段 | self/admin | support（客服） | family/agent | other |
|---|---|---|---|---|
| 手机号 | 明文 | 明文（需联系） | 138****5678 | 138****5678 |
| 身份证 | 明文 | 110101********123X | 同左 | 同左 |
| 银行卡 | 明文 | 6222***********0123 | 同左 | 同左 |
| 邮箱 | 明文 | ab***@x.com | 同左 | 同左 |
| 姓名 | 明文 | 张** | 张** | 张** |
| 地址 | 明文 | 到市（上海市****） | 到区（上海市浦东新区****） | 到区 |
| 金额 | 精确到分 123.45 | 到元 123 | *** | *** |

## 2. 字段名白名单驱动（避免误伤）

- 仅对**已知名单字段**脱敏：`phone/mobile/tel/contact`、`id_card`、`bank/card`、`email/mail`、`address/addr`、`amount/cents/price/fee/balance`、`real_name/name`。
- 仅对**声明的自由文本字段**做内嵌正则扫描：`content/subject/body/text/note/feedback/message`。
- 其他业务标识（id / ticket_no / 编号 / 时间戳）**原样保留**，防止长数字 ID 被银行卡规则误脱敏。

## 3. 自由文本正则

按优先级：身份证(18 位) → 银行卡(16–19) → 手机(11 位) → 邮箱；前一步替换为 `*` 后不再被后续数字规则命中。

## 4. 出口集成与审计

`GET /v1/egress/tickets?role=agent`（管理员 Token）
- 真实读 `support_tickets` → 按角色脱敏 → 落 `security_events(event_type='egress_masked')`（含角色、脱敏字段数、工单数）。
- 缺省 role=other（最小暴露）。

## 5. 复现

```bash
go test ./internal/masking/ -run TestV185 -count=1 -v
# TestV185FieldMaskers（单字段）/ TestV185RoleWhitelist（白名单回落）/
# TestV185FreeTextScan（自由文本）/ TestV185EgressTicketsReal（真实读库+脱敏+落审计，零残留）
```

## 6. 与加密存储的关系

- 落库：`users.phone_encrypted` 加密存储（不存明文）；
- 出端：本包负责"展示层脱敏"；二者构成"存储加密 + 出口脱敏"双保险。
