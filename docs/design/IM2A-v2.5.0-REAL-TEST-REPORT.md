# IM2A v2.5.0 真实开发与实测报告

日期：2026-10-31
版本：v2.5.0（tag b5cead0；后续硬化提交 fc99720）

## 1. 开发进度

| 能力 | 状态 |
|---|---|
| Agent Plugins 2.0 源清单 | 完成（profileVersion 2.0） |
| 1.0 -> 2.0 投影 | 完成并通过 |
| 技能运行时（提案/暂存/晋升/回滚） | 完成（内存参考实现） |
| 真实 SQLite 落库 | 完成并通过 6/6 |
| 时间戳/不可变包/唯一约束/事务 | 完成 |
| 零残留测试 | 完成（DB/WAL/SHM/目录均删除） |
| Go gateway build/vet/test | 完成（Linux go1.27.3） |
| 生产 Postgres 发号服务 | 未完成 |
| Hermes Python 进程/ACP | 未接入 |
| 真实 RL 训练 | 默认关闭，未执行 |
| 六方真实 E2E | 仅既有浏览器/DSH 证据；其余待补 |

## 2. 代码质量

### Go（Vultr Linux，go1.27.3）

```text
go build ./...  exit 0
go vet ./...    exit 0
go test ./...   exit 0
73 packages ok, 0 FAIL
```

修复的真实缺陷：

```text
internal/memoryvault/vault.go:92
self-assignment of scope.UserID
```

修复后 build/vet/test 全部重新通过。

### Node / 契约

```text
validate：PASS
P0：4/4 PASS
ident-code：16/16 PASS，连续 20 次 PASS
skill-runtime：8/8 PASS
SQLite：6/6 PASS
bridge/trajectory/training/boundary/realtime：PASS
release dry-run：PASS
release allow-list：PASS
```

## 3. SQLite 落库断言

测试覆盖：

```text
真实文件 DB
关闭/重开持久化
唯一约束与事务
proposal 不修改 active
base mismatch 拒绝
eval floor / evidence 闸门
promotion 写 package + active + ledger + idempotency
rollback 指针移动
SQLite 中 curator 不能 promote/rollback
DB/WAL/SHM/目录零残留
```

性能实测（Node 24.16，本地真实 SQLite 文件）：

```text
register: avg 6.52ms, p50 6.37ms, p95 7.18ms (n=1000)
list:     avg 3.73ms, p50 3.63ms, p95 4.24ms (n=1000)
view:     avg 0.21ms, p50 0.20ms, p95 0.26ms (n=1000)
promote:  avg 18.51ms, p50 18.02ms, p95 21.06ms (n=100)
```

完整性：

```text
activeSkills=1000
ledgerRows=1100
idempotencyRows=100
databaseRemoved=true
directoryRemoved=true
```

## 4. 功能实测结论

```text
Agent Plugins 2.0 自描述/投影：PASS
技能提案不会直接改 active：PASS
晋升闸门与幂等：PASS
回滚：PASS
渐进披露 L0/L1/L2：PASS
session pin：PASS
SQLite 真实持久化：PASS
零残留：PASS
Go gateway 全量 build/vet/test：PASS
```

## 5. 复盘与风险

1. 本轮之前有随机测试假阴性（tamper 字符可能未变化、Luhn 替换可能 no-op），已改为确定性负控并连续 20 次验证。
2. Go vet 发现并修复了 memoryvault 的 self-assignment；说明“测试全绿”不等于“静态检查全绿”。
3. SQLite 是真实数据库验证，但不是生产 Postgres；QPS、连接池、复制、PITR、RTO 未测。
4. 未部署生产服务：v2.5.0 没有可直接启动的 gateway/技能服务产物；不应把本地 SQLite/Go 构建冒充线上部署。
5. Hermes/ACP/真实 RL 未接入；只发布契约与默认关闭边界。
6. healthz 在 v2.5.0 发布范围外，未声称通过。

## 6. 发布状态

```text
v2.5.0 tag：b5cead0
v2.5.0 GitHub Release：已发布
修复/实测提交：fc99720（release/2.4）
npm DSH plugin 0.2.0：已发布
生产部署：未执行
```

## 7. 结论

```text
真实开发：YES（SQLite 落库、Go 修复）
真实数据库断言：YES（SQLite）
零残留：PASS
全量 build/vet/test：PASS
生产部署：NO
质量结论：可进入 v2.5.1 补丁验证，不应宣称 v2.5.0 已生产上线
```
