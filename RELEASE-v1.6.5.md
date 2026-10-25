> ⛔ **历史记录，其中的地址为用户生产机，禁止连接。**
>
> 本文件中出现 `106.53.197.47` 的内容均为历史证据（正文保留、不回改）；禁止 ssh/scp/curl/端口探测/日志读取/写入（含"只读诊断"），不得作为可执行目标。用户硬指令（2026-10-25 起生效）：该 IP 不连接、不读取、不写入。

# IM2A v1.6.5 — 五方发布（CI · Release · Compat · GitHub Release · npm）

## 本版本真实能力（生产 e2e 验收）
- **腾讯云 IM 真实打通**：官方 TLSSig v2 UserSig（zlib + URL-safe base64）、account_import、跨账号 C2C（真实 MsgKey）、真实建群 CreateGroup、群发 SendGroup（MsgSeq 兜底）。
- **Webhook 回调接收**：真实回调字段归一（CallbackCommand/MsgId、MsgBody 数组）、?key 验签、SdkAppID 校验、im_webhook_events 幂等落库；坏 key → 401。修复 3 个真实解析 bug。
- **付费账本收口**：v0.9 老表 12 行/80 分确定性迁移到 paid_message_ledger，老表归档 payment_ledger_legacy_v09。
- **v1.6.5 发布流水线**：统一版本 single-source（/healthz 暴露 version+commit）、6 平台二进制、校验和、Compat 兼容矩阵、npm 跨平台启动器。

## 五方发布
| 方 | 内容 |
|---|---|
| CI | .github/workflows/ci.yml：build / vet / test -race / 7 服务 selftest |
| Compat | .github/workflows/compat.yml：9 proto 契约 + 版本一致 + 核心路由 + 6 平台矩阵编译 |
| Release workflow | .github/workflows/release.yml：打 tag 触发门禁→多平台构建→冒烟→GitHub Release |
| GitHub Release | v1.6.5，6 平台二进制 + checksums.txt |
| npm | 包 im2agent（跨平台启动器，npx/npm i -g；首次从 Release 下载二进制） |

## 验证证据（2026-10-23 实跑）
- go build ./... / go vet ./... 全通过
- go test -race ./... 全部 ok，无 FAIL
- 7 服务 selftest 全部 PASS（消息分片锁/信任门控/审批状态机/协商熔断/幂等账本/出口护栏/编排闭环）
- make compat：ALL CHECKS PASS（6 平台）
- npm 启动器 e2e：IM2A_BIN 透传启动 → /healthz 返回 v1.6.5
- 生产网关 106.53.197.47 /healthz：version=v1.6.5

## 资产
- im2a-gateway_{linux,darwin,windows}_{amd64,arm64}（windows 含 .exe）
- checksums.txt（SHA-256）

## 已知闸门（诚实标注）
- 腾讯云 webhook 公网 HTTPS 投递需域名 + TLS 证书（v1.7.0 资源闸门）。
- npm publish 到 registry 需在仓库配置 NPM_TOKEN；包已就绪、npm pack 与本地 e2e 已验证。
