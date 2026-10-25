# IM2A v1.8.8 — 五方发布（CI · Release · Compat · GitHub Release · npm）

> 人机混合熟人社交 IM：Agent 承担协调/预约/确认的「行政层」，人类只处理情感层与最终决策层。

## 本版本定位

v1.8.8 是 **v1.8 安全合规线的收口与稳定发布版**。在 v1.8.7 全版本代码审查（v1.0.0–v1.8.7）基础上，修复了一项不可变审计（WORM）合规红线与测试零残留缺陷，完成版本 single-source 收口与五方发布。

## 本版本真实变更

### 门禁 `-race` 实测缺陷修复（发布前 `make gate` 暴露并修复）
- 迁移并发死锁：`pg_advisory_lock` 串行化 001–015 DDL，消除多实例/并行测试 SQLSTATE 40P01。
- 推送重试越权：`retry?user_id=X` 由全量调度改为按用户 `FOR UPDATE SKIP LOCKED`；500 失败不再误标 sent，保持 pending。
- Postgres 存储测试长期误 skip：默认 pgx、按外键顺序建用户、级联 + 前缀兜底，真实落库断言得以执行。
- 推送测试跨进程串扰：v173/v174 用 advisory lock 串行化。
- 移除硬编码生产 admin token：仅经环境变量注入，未设置则跳过 live 套件（带 token 8 项渗透断言全 PASS）。
- 全新缓存连续 2 轮 `make gate` GATE_EXIT=0，0 FAIL/0 race/0 deadlock，27 包 ok、全业务表零残留。

### 安全 / 合规
- **移除 5 处对 WORM 审计触发器的绕过**（`DISABLE TRIGGER USER → DELETE → ENABLE`）：运营后台测试不再临时关闭 append-only 审计，改为唯一 `trace_id`/`ref` 数据隔离，触发器全程启用。
- **根治全量并行测试偶发 FAIL**：触发器全局切换正是 `privacy.TestV186ExportAndErase` 偶发失败的根因；修复后连续 3 轮全量测试 0 FAIL / 0 panic。
- 约 20+ 测试补齐 `t.Cleanup` 零残留；干净库跑全量前后业务表行数完全一致。
- 修正 IM / 推送模块 5 处静默吞错。
- 历史库一次性清理（22 张业务表残留 + 孤儿用户 + 脏工作记忆）。

### 工程化
- 版本 single-source 收口 **v1.8.8**（version.go / Makefile / package.json / npm 启动器 / README）。
- 新增 `CHANGELOG.md`；全仓库 `gofmt`（33 文件）；非测试代码无 `TODO/FIXME/HACK`。

## 五方发布

| 方 | 内容 |
|---|---|
| CI | `.github/workflows/ci.yml`：build / vet / test -race / 8 服务 selftest |
| Compat | `.github/workflows/compat.yml`：9 proto 契约 + 版本一致 + 核心路由 + 6 平台矩阵编译 |
| Release workflow | `.github/workflows/release.yml`：打 tag 触发门禁 → 多平台构建 → 冒烟 → GitHub Release |
| GitHub Release | v1.8.8，6 平台二进制 + checksums.txt |
| npm | `@twinsearth/im2agent@1.8.8` 跨平台启动器（`npx @twinsearth/im2a` / `npm i -g`） |

## 验证证据（2026-10-24 实跑）

- `go build ./...` / `go vet ./...`：通过；`gofmt -l`：0 遗漏。
- `go test ./...`：37 包全 ok（含 `-race`），0 FAIL / 0 panic，连续 3 轮 ALL OK。
- `make gate`：build / vet / race 测试 / 8 服务自测全 PASS。
- `make compat`：9 proto 契约 + 版本与 healthz 一致 + 核心路由在场 + 6 平台矩阵，ALL CHECKS PASS。
- 生产网关 `https://geefish.cn/healthz`：`version=v1.8.8`。

## 资产

- `im2a-gateway_{linux,darwin,windows}_{amd64,arm64}`（Windows 含 `.exe`）
- `checksums.txt`（SHA-256）

## 已知闸门（诚实标注）

- Flutter 客户端需在具备 Flutter SDK 的机器执行 `flutter create . && flutter analyze` 补齐平台目录；真机 BLE 需穿戴硬件。
- 腾讯云 IM 主动 API live 测试、APNS 真机推送需对应凭据/证书与真机。
- 早期设计的 gRPC/Kafka/pgvector 未引入，实际走 A2A over HTTP + 事件流；OpenSearch 低内存环境默认关闭。
