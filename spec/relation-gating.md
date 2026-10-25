# Relation Gating Protocol v1（关系档位门控：实现无关规范）

> 状态：v1.0（2026-10-25）｜ 语义源实现：`internal/trustzone/trust.go`（Go）
> 配套：公开测试向量 `spec/vectors/relation-gating.v1.json`、JSON Schema `spec/vectors/schema.json`、
> 一致性套件 `conformance/` 与命令行 `cmd/relation-conformance`。
>
> **本文档是"实现无关"的协议描述**：任何语言/任何实现，只要对同一输入给出相同输出，即视为符合本规范。
> 规范中的每个不变量都至少对应一条公开向量（见 §5 的向量 ID）。

---

## 0. 目的、范围与非目标

**目的**：把"关系档位（信任等级）× 能力领域（scope）"的访问门控，变成可被第三方独立验证的协议：
输入/输出模型、判定顺序、权限矩阵、reason code、边界语义全部写死，并提供可执行的公开向量。

**范围（in scope）**：一次工具调用前的**纯函数判定** `decide(capability, call) → decision`。

**非目标（out of scope）**：
- 信任档位如何建立/变更（关系服务职责）；
- 审批流、审计、通知；
- 日预算的**累计**记账（本协议的 `budgetCentsPerDay` 在一处判定里被当作**单次调用金额上限**使用；
  日累计由调用方/上层实现，属实现细节，见 §6.3）；
- 并发、幂等、缓存（判定是纯函数：相同输入必须永远相同输出，与调用时序无关）。

---

## 1. 输入模型

```jsonc
{
  "capability": {
    "agentId": "string",              // 仅用于审计/日志，不参与判定
    "trustLevelMin": 0,               // 整数档位下限；见 §1.1
    "allowedSkills": ["skill.a"],     // Skill 白名单；空数组/缺省 = 不限制
    "budgetCentsPerDay": 0,           // 单次金额上限（分）；0 = 不启用
    "requiresApprovalAboveCents": 0   // 金额审批阈值（分）；0 = 不启用
  },
  "call": {
    "skillId": "skill.a",             // 被调用的 skill 标识
    "scope": "schedule",              // 能力领域；见 §1.2（开放字符串）
    "trustLevel": 0,                  // 本次会话的信任档位；见 §1.1
    "amountCents": 0,                 // 涉及金额（分）；0 = 无金额
    "targetUid": 12345                // 目标用户；不参与判定（见 I6）
  }
}
```

字段缺省规则（缺失等价于下列默认值，第三方实现必须一致）：

| 字段 | 缺省 | 说明 |
|---|---|---|
| `capability.trustLevelMin` | `0` | 0 = L0，不设下限 |
| `capability.allowedSkills` | `[]`（空） | 空 = 不限制任何 skill |
| `capability.budgetCentsPerDay` | `0` | 0 = 不启用预算检查 |
| `capability.requiresApprovalAboveCents` | `0` | 0 = 不启用金额审批阈值 |
| `call.skillId` | `""` | 空字符串是合法 skill 标识（除非白名单显式列了非空集合） |
| `call.amountCents` | `0` | 负数属**输入域外**，行为见 §6.2 |
| `call.targetUid` | `0` | 不参与判定 |

### 1.1 信任档位 Level（整数，规范定义 0..4）

| 值 | 名称 | 含义（非规范性说明） |
|---|---|---|
| 0 | `self` | 本人：全部自主 |
| 1 | `family` | 家庭：日程/家务/采购自主；大额与对外承诺需审批 |
| 2 | `trusted` | 信任：日程自主；其余多为审批 |
| 3 | `known` | 已知：几乎全审批 |
| 4 | `stranger` | 陌生：仅结构化来意可审批 |

- **输入是整数**（实现无关）；名称仅作文档别名，第三方实现不应要求调用方传名称。
- `0..4` 之外的整数（如 `-1`、`5`、`99`）是合法输入但语义见 §5/I2。

### 1.2 能力领域 Scope（字符串，规范定义 7 个）

`schedule`（日程）、`family`（家庭事务/物业）、`purchase`（采购比价）、`workmail`（工作邮件）、
`stranger`（陌生人过滤）、`finance`（资金/大额）、`external`（对外承诺/发送）。

- **scope 是开放字符串**：未知 scope 不报错，按 §5/I1 得到 `forbidden`。
- 大小写/空白：**不做归一化**，`"Schedule"` 与 `"schedule"` 视为不同 scope（后者已知、前者未知）。
  需要归一化是调用方责任，本协议不定义。

---

## 2. 输出模型

```jsonc
{
  "allowed": false,          // 是否可直接执行
  "requiresApproval": true,  // 是否需人工审批（allowed=false 时可能为 true）
  "reasonCode": "SCOPE_REQUIRES_APPROVAL"
}
```

### 2.1 三态与 reason code

| # | `allowed` | `requiresApproval` | `reasonCode` | 语义 |
|---|---|---|---|---|
| 1 | `true` | `false` | `OK` | 放行 |
| 2 | `false` | `true` | `SCOPE_REQUIRES_APPROVAL` | 矩阵要求审批（步骤 3） |
| 3 | `false` | `true` | `AMOUNT_ABOVE_THRESHOLD` | 金额超过审批阈值（步骤 4） |
| 4 | `false` | `false` | `SKILL_NOT_ALLOWED` | skill 不在白名单（步骤 1） |
| 5 | `false` | `false` | `TRUST_BELOW_MIN` | 档位低于下限（步骤 2） |
| 6 | `false` | `false` | `SCOPE_FORBIDDEN` | 矩阵禁止，或未知 scope/level 的缺省禁止（步骤 3） |
| 7 | `false` | `false` | `AMOUNT_ABOVE_BUDGET` | 金额超过单次预算上限（步骤 5） |

**三态约束（不变量 I6）**：
- `allowed=true` ⟺ `requiresApproval=false && reasonCode=OK`；
- `requiresApproval=true` ⟹ `allowed=false`，且 reasonCode ∈ {2,3}；
- 其余拒绝（reasonCode ∈ {4,5,6,7}）必须 `requiresApproval=false`。

> 第三方实现只需保证上述**七元组**（allowed/requiresApproval/reasonCode）一致；
> 人读消息文本（如 `"scope finance forbidden at trust known"`）**不属于协议**，可以任意。

---

## 3. 判定顺序（ normative：顺序影响结果）

`decide` 必须按下列**五步顺序**执行，**第一个命中即返回**，不得跳过或重排：

```
decide(capability, call) -> Decision:
  1. skill 白名单
     if len(capability.allowedSkills) > 0 and call.skillId not in allowedSkills:
         return DENY SKILL_NOT_ALLOWED

  2. 信任档位下限
     if call.trustLevel < capability.trustLevelMin:
         return DENY TRUST_BELOW_MIN

  3. 领域矩阵（含未知 scope/level 的缺省 forbidden）
     perm = matrix[call.trustLevel][call.scope]        # 查不到 = forbidden
     if perm == forbidden:            return DENY      SCOPE_FORBIDDEN
     if perm == requires_approval:    return APPROVAL  SCOPE_REQUIRES_APPROVAL
     # perm == auto_execute 才继续

  4. 金额审批阈值（严格大于）
     if capability.requiresApprovalAboveCents > 0
        and call.amountCents > capability.requiresApprovalAboveCents:
         return APPROVAL AMOUNT_ABOVE_THRESHOLD

  5. 单次预算上限（严格大于）
     if capability.budgetCentsPerDay > 0
        and call.amountCents > capability.budgetCentsPerDay:
         return DENY AMOUNT_ABOVE_BUDGET

  return ALLOW OK
```

### 3.1 顺序优先级（必须写死，向量 §A2 覆盖）

| 冲突 | 结果（先命中者） |
|---|---|
| 白名单拒绝 vs 其它任何 | `SKILL_NOT_ALLOWED`（步骤 1 最先） |
| 档位不足 vs 矩阵禁止/审批/金额/预算 | `TRUST_BELOW_MIN`（步骤 2） |
| 矩阵禁止 vs 金额阈值/预算 | `SCOPE_FORBIDDEN`（步骤 3 先于 4/5） |
| 矩阵要求审批 vs 金额阈值 | `SCOPE_REQUIRES_APPROVAL`（步骤 3 先于 4） |
| 金额超阈值 **且** 超预算 | `AMOUNT_ABOVE_THRESHOLD`（步骤 4 先于 5）——**不是**预算拒绝 |
| 金额超预算（未超阈值/阈值未启用） | `AMOUNT_ABOVE_BUDGET` |

> 注意"金额超阈值且超预算 ⇒ 审批而非拒绝"是**规范行为**（步骤 4 的提前返回），不是优化空间；
> 若实现想要更严格，必须作为新版本协议（v2）显式变更，不得在 v1 内静默偏离。

---

## 4. 权限矩阵（5 × 7，normative）

`A` = `auto_execute`，`P` = `requires_approval`，`F` = `forbidden`（**缺省 = F**，表内不写 F 的格子表示缺省）。

| Level \ Scope | schedule | family | purchase | workmail | stranger | finance | external |
|---|---|---|---|---|---|---|---|
| **0 self** | A | A | A | A | A | A | A |
| **1 family** | A | A | A | **F** | P | P | P |
| **2 trusted** | A | P | P | **F** | P | P | P |
| **3 known** | P | **F** | **F** | **F** | P | **F** | P |
| **4 stranger** | **F** | **F** | **F** | **F** | P | **F** | **F** |

（向量 `m-L{0..4}-{scope}` 逐格覆盖 35 格。）

---

## 5. 不变量（每条都有对应公开向量）

| # | 不变量 | 向量 ID |
|---|---|---|
| **I1** | **未知 scope ⇒ forbidden（`SCOPE_FORBIDDEN`）**，前提是未被步骤 1/2 提前拒绝；对 L0 同样成立（L0 只对 7 个已知 scope 全 auto） | `unk-scope-at-L0`, `unk-scope-at-L4`, `unk-scope-with-amount` |
| **I2** | **未知 level ⇒ 矩阵缺省 forbidden（永不 auto）**；但若 `trustLevel < trustLevelMin`（如 level=-1、min=0），步骤 2 先命中 → `TRUST_BELOW_MIN`（仍非 auto）。level=5/99 在 min=0 时走 §3 步骤 3 → `SCOPE_FORBIDDEN` | `unk-level-99`, `unk-level-5`, `unk-level-neg1-with-min0` |
| **I3** | **L4（stranger）在任何 scope/金额/预算下都不得 `auto_execute`**（唯一可能放行前状态是审批） | `inv-L4-no-auto-*`（7 条） |
| **I4** | **L0（self）对 7 个已知 scope 全为 `auto_execute`**（在白名单通过且金额未超阈值/预算时；L0 + 未知 scope 仍 forbidden，见 I1） | `inv-L0-all-auto-*`（7 条，与矩阵格同输入不同 ID） |
| **I5** | **单调性：固定 scope，档位越高（4→0）不得更严格。** 定义 rank(`auto_execute`)=2 > rank(`requires_approval`)=1 > rank(`forbidden`)=0，要求 `rank(L_{k+1}) ≥ rank(L_k)`。**已逐 scope 验证 7/7 成立，无例外**（见 §5.1 与向量 `mono-*`） | `mono-*-L4..L0`（成对） |
| **I6** | **纯度/三态一致性**：判定只依赖 §1 的输入字段（`targetUid`、`agentId` 不影响结果），且三态与 reasonCode 的关系必须满足 §2.1；相同输入永远得到相同输出（无状态） | `pure-targetuid-ignored`, `pure-repeatable`（同输入重复出现） |
| **I7** | **边界语义**：`requiresApprovalAboveCents=0` / `budgetCentsPerDay=0` 表示**不启用**该步；金额比较用**严格大于**（等于不放行/不审批）；`amountCents=0` 永不因金额被拒 | `bnd-*`（见 §6） |

### 5.1 单调性核验结果（实现 `trustzone` 当前版本）

逐 scope、按 L4→L3→L2→L1→L0 读取矩阵 rank：

| scope | L4 | L3 | L2 | L1 | L0 | 结论 |
|---|---|---|---|---|---|---|
| schedule | F(0) | P(1) | A(2) | A(2) | A(2) | 单调 ✓ |
| family | F | F | P | A | A | 单调 ✓ |
| purchase | F | F | P | A | A | 单调 ✓ |
| workmail | F | F | F | F | A | 单调 ✓ |
| stranger | P | P | P | P | A | 单调 ✓ |
| finance | F | F | P | P | A | 单调 ✓ |
| external | F | P | P | P | A | 单调 ✓ |

→ **无例外**：不存在"更高档位反而更严格"的格子。已知的唯一"看起来更严格"的误读是
**I1 的未知 scope**：L0 对未知 scope 是 `forbidden`（而某些低档位对同名字符串也不可能 auto），
这不是单调性破坏，因为未知 scope 不在 7 个可比 scope 的域内。

---

## 6. 边界与输入域

### 6.1 金额相关

| 场景 | 结果 | 说明 |
|---|---|---|
| `amount=0`，任意阈值/预算 | 不受金额影响 | `0 > x` 永不成立（x≥0） |
| `amount == threshold`（>0） | **不**触发审批 | 严格大于 |
| `amount == budget`（>0） | **不**拒绝 | 严格大于 |
| `threshold=0` | 步骤 4 不启用 | |
| `budget=0` | 步骤 5 不启用 | |
| `amount = MaxInt64` | 按上面规则判定，不溢出 | 实现须用有符号 64 位比较，不做乘法 |

### 6.2 输入域外（负数金额）

`amountCents < 0` **不在输入域内**（金额语义不允许负）。协议对它的要求只有一条：
**不得因此产生 `allowed=false, requiresApproval=false, reasonCode ∉ {…}` 之外的意外**——即实现必须给出
七种 reasonCode 之一。参考实现的行为：负数不可能大于任何正阈值/预算，因此通常得到 `OK`
（向量 `bnd-negative-amount` 记录参考实现的这一行为；第三方实现若选择拒绝（如 `AMOUNT_ABOVE_BUDGET`）
**不属于 v1 合规**，需要显式声明扩展；因此 v1 要求与参考行为一致）。
生产调用方应先做输入校验（金额≥0），本协议提供该行为只是为了让判定成为全函数。

### 6.3 预算语义澄清

`budgetCentsPerDay` 名字来自存储字段（历史兼容）。在本协议的一次判定中它被解释为
**单次调用金额上限**；日累计扣减不在此函数内完成（否则判定不再是纯函数）。第三方实现若做日累计，
必须把它放在 `decide` 之外，并保证同一输入在预算未变化时输出一致。

### 6.4 白名单

| 场景 | 结果 |
|---|---|
| `allowedSkills` 空（缺省） | 不限制 skill（步骤 1 跳过） |
| `allowedSkills=["a"]`，`skillId="a"` | 通过（精确匹配，区分大小写） |
| `allowedSkills=["a"]`，`skillId="A"` | `SKILL_NOT_ALLOWED` |
| `allowedSkills=[""]`，`skillId=""` | 通过（空字符串是合法标识） |

---

## 7. 与参考实现的关系（reference implementation）

- 本协议由 `internal/trustzone.Capability.Check` 实现（Go）。它返回 `{Allowed, RequiresApproval, Reason(人读文本)}`。
- **人读文本不在协议内**。一致性套件的 reference adapter（`conformance/reference.go`）负责把文本映射为
  §2.1 的 reasonCode（按消息前缀：`skill `/`trust `/`scope `/`amount ` + "exceeds threshold"/"exceeds daily budget"）。
- 因此：**新增/修改文本不改变协议**；但若改动了判定顺序或矩阵，必须 bump 协议版本并更新向量。

---

## 8. 如何用公开向量验证你的实现

```bash
# 用本仓库的参考实现跑（CI 用这条）
go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference

# 机器可读输出（给 CI/第三方集成）
go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference -json
```

第三方接入自己的实现：实现 `conformance.Implementer` 接口（1 个方法），见
`conformance/README.md` 与 `conformance/example/stub.go`（10 行 stub）。任何向量不一致 → 进程 exit 1。

向量文件版本化：文件名 `relation-gating.v1.json`，`version` 字段固定 `1`；不兼容变更必须新文件名。
