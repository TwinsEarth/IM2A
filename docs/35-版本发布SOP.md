# 35 · 版本发布 SOP（IM2A 主线）

> 适用：`main`（v0.1.3 线）· 维护者：技术负责人 · 审核：用户（Stable 只能由用户批准）
> 本文替代此前散落在 docs/30、docs/34 的发布步骤，作为**唯一发布操作手册**。
> 原则：**SOP 只写能执行、能验证的步骤；不适用的一律标 N/A 并说明原因，不假装有这条流水线。**

---

## 0. 发布前条件（4 项 checklist，缺一不发布）

| # | 条件 | 判定命令/证据 | 不满足时 |
|---|---|---|---|
| 0.1 | 工作区干净、在 `main`、已同步 | `git status --short` 空；`git log -1 --oneline` 与 `origin/main` 一致 | 先 rebase/合并，不允许带未提交改动发布 |
| 0.2 | 门禁全绿 | `node scripts/release-check.mjs` 返回 0（6/6 步） | 按 §3 红灯表修复，禁止跳过 |
| 0.3 | 版本三元组一致 | `GET /api/version` = `version.mjs` = `package.json` = 即将打的 tag | 按 §1 四处同步 |
| 0.4 | 数据变更可控 | 无 schema 变更，或已提供 `scripts/migrate-state.mjs` 迁移且演练过 | 无迁移脚本 → 不发布带 schema 变更的版本 |

> Stable（对真实用户开放）还需用户批准，见 §5。Agent 不得自批。

---

## 1. 版本 bump（本仓库 4 处；Go/Flutter/npm 为 N/A）

| 位置 | 文件 | 本仓库做法 | 备注 |
|---|---|---|---|
| ① 运行时版本三元组 | `apps/im2agent/version.mjs` | 改 `VERSION`；契约有变更时改 `CONTRACT_REVISION`（R#） | `/api/version`、`/api/health` 同源 |
| ② 包元数据 | `package.json` | `version` 与 ① 完全一致 | 未发布 npm（`private: true`），**不需要 npm publish** |
| ③ 契约变更记录 | `contracts/INTERFACES.md` | 新增 `R#` 行（日期/变更/是否加法） | 无契约变更则跳过 |
| ④ 首页状态与徽章说明 | `README.md` §8 状态表 + 徽章说明 | 更新测试数/验收数/版本三元组；徽章不含版本号（npm 未发布） | 防止"徽章是新的、数字是旧的" |
| — | Go 二进制版本 | **N/A** | 本主线无 Go 代码；Go/Flutter 线在独立的 Mac mini 交付线 |
| — | Flutter `pubspec.yaml` | **N/A（未编译）** | `client/` 是未编译骨架，不参与本 SOP |
| — | npm registry | **N/A（private）** | 不发布；写 `npm/v` 徽章会指向不存在的包 |
| — | README 版本徽章 | **N/A** | 同上；用静态 npm「zero dependencies」徽章替代 |

**一致性检查（自动化）**：`scripts/verify.mjs` 已校验 `package.json.version === version.mjs VERSION`；不一致直接红。

---

## 2. CHANGELOG（`CHANGELOG.md`）

方向：**newest-first**（最新在最上）。每条必须写清四项，缺一项视为不完整：

| 必写项 | 例子 |
|---|---|
| 改了什么 | "恢复备份 schema 闸门：不兼容 → 409" |
| 为什么 | "验签通过 ≠ 能用，用户拿到手才发现不可恢复" |
| 证据 | "测试 ㉑；`--filter=migrate` 7 passed" |
| 未做/缺口 | "审核案与争议仍为内存态（docs/32）" |

> 禁止只写"优化/修复若干问题"。禁止把"证据补强"写成"修复"（见 docs/33 §4）。

---

## 3. 打包与门禁（Node 主线）

### 3.1 门禁命令（顺序固定）

```powershell
node scripts/test.mjs            # 全量测试（732 passed）
node scripts/verify.mjs          # 文档/契约/代码一致性（134/134）
node scripts/deploy-lint.mjs     # 部署产物静态检查（14/14）
node scripts/quickstart.mjs --check   # 一键自检（13/13）
node scripts/release-check.mjs   # 以上全部 + 内置实例验收 + 版本↔tag（6/6）
```

### 3.2 打包产物（可选，用于归档/离线交付）

```powershell
git archive --format=zip --prefix=im2a-vX.Y.Z/ -o dist/im2a-vX.Y.Z.zip HEAD
# 记录 sha256（资产核验用，见 §6）
Get-FileHash dist/im2a-vX.Y.Z.zip -Algorithm SHA256
```

> 不做 Go 交叉编译、不打 Docker 镜像（本机无 Go/Docker；`deploy/` 是配置交付物，非构建产物）。

### 3.3 红灯修法表

| 红灯 | 常见原因 | 处置 |
|---|---|---|
| `test.mjs` 失败 | 改动了行为未改测试；或环境（Node 版本） | 先复现→写红测试→修；Node 必须 24（`node:sqlite`） |
| `verify.mjs` 失败 | 文档/契约/代码漂移；新增模块未声明「模块职责」首行 | 按失败项修文档或补声明，**不改校验迁就代码** |
| `deploy-lint` 失败 | 部署脚本缺必备项/含 Tab | 修脚本，不降低检查 |
| `quickstart --check` 失败 | 服务起不来/自检断言失败 | 看 stderr，先修启动再修断言 |
| `release-check` tag 不一致 | VERSION 与 HEAD tag 不匹配 | 正常流程：先 bump + 提交 → 打 tag → **复跑** release-check |
| CI 红 | 平台差异（Node 版本/路径） | 以 CI 日志为准；本地绿≠CI 绿，二者都要绿 |

---

## 4. 提交与打 tag

### 4.1 标准路径（推荐，本机可用）

```powershell
git add -A
git commit -F <utf8-no-bom 的消息文件>    # 中文消息必须用文件，避免 PowerShell 编码乱码
git tag -a vX.Y.Z-fix -m "vX.Y.Z: 一句话"
git push origin main
git push origin vX.Y.Z-fix
```
**tag 命名**：`v0.1.N-fix`（缺陷修复）/ `v0.2.0`（能力版本）；必须与 `VERSION` 前缀一致，release-check 会核对。

### 4.2 API 路径（仅当 git 推送不可用）

GitHub Contents API 写文件 + Git refs API 建 tag。本机对 `api.github.com` 不通（代理限制），此路径**未在本机验证**，仅在 CI/其他网络环境使用：

```
PUT  /repos/{owner}/{repo}/contents/{path}      # 单文件（base64 + sha）
POST /repos/{owner}/{repo}/git/refs             # {"ref":"refs/tags/vX.Y.Z","sha":"<commit>"}
```
> 红线：API 建 tag 前必须确认 commit 已存在；禁止只建 tag 不推 commit。

---

## 5. Stable 审批（只能用户做）

| 项 | 规则 |
|---|---|
| 谁能批 | **只有用户/产品负责人**；Agent 不得自批、不得代签 |
| 批什么 | 是否允许把该版本作为 Stable 对真实用户开放 |
| 前置 | §0 四项全绿 + 真实环境预检（`accept.mjs` 预检 7/7）+ 已知缺口公告（docs/30 §6） |
| 记录 | 在 GitHub Release 正文或发布记录里写"批准人 + 时间 + 范围"，可追溯 |
**人工签核入口**：33 条业务 Gate 用 Issue 模板逐条勾选（含证据链接与例外说明）：
[新建 Gate 签核 Issue](https://github.com/TwinsEarth/IM2A/issues/new?template=gate-signoff.yml) ·
模板文件 `.github/ISSUE_TEMPLATE/gate-signoff.yml`。
未勾选项必须在「例外与豁免」里写明原因/影响/批准人/补救时间，否则视为未签核。
| 拒绝后 | 保持 RC/预发，不对外宣称 Stable；缺口修完再申请 |

---

## 6. 五方核验（发布后逐项核对）

| # | 核验方 | 本仓库判定 | 命令/入口 |
|---|---|---|---|
| 6.1 | npm | **N/A（private，不发布）** | 不写 npm 版本徽章；`package.json` 只作元数据 |
| 6.2 | GitHub Release | 可选（未创建 Release 时标 N/A） | API/网页创建，正文含版本三元组与缺口摘要 |
| 6.3 | 发布资产 | 有 `dist/*.zip` 时核 `sha256` 与记录一致 | `Get-FileHash` |
| 6.4 | Packages | **N/A（无包注册表）** | — |
| 6.5 | CI | 最新 push 的 run 为 success | Actions 页 / API 查询 |
| 6.6 | 兼容性 | `/api/version` 三元组 = package.json = tag；schema 与备份可迁移 | `accept.mjs` 的 `G-APP`/预检 + `verify` |

> 五方里 **npm / Packages 在本主线永久 N/A**，不是"待办"；写成待办会误导后续维护者。

---

## 7. 生产部署与回滚

### 7.1 部署（Linux）

```bash
sudo ./deploy/linux/install.sh /opt/im2a-repo      # dry-run 先演练
systemctl status im2a
node scripts/accept.mjs --target http://127.0.0.1:8080 --token "$ADMIN_TOKEN" \
  --viewer-token "$VIEWER_TOKEN" --require-real-providers   # 预检 + Gate
```
公网：`deploy/prod/`（Caddy 自动 HTTPS + `ADMIN_TOKEN` + 安全头），反代后必设 `TRUST_PROXY=1`。

### 7.2 回滚（两条同时满足）

| 层 | 动作 |
|---|---|
| 代码 | `git switch --detach v上一版` 重新 `install.sh`（禁止用 force push 回滚 main） |
| 数据 | 停写 → 用备份 `restore` 验签（**不自动应用**，人工确认）→ 若 schema 不兼容先迁移副本 → 对账 `GET /api/ops/reconcile` |

---

## 8. 收尾（证据入库 + 代替关系）

1. `reports/RELEASE-REPORT.md` 提交入库（含五步耗时、版本三元组、结论 PASS/FAIL）；
2. README §8 状态表更新（版本 / 测试数 / 未验证项）；
3. **代替关系**：新 SOP 与文档的关系写清，避免双份手册冲突：
   - 本文（docs/35）= 发布操作 SOP（唯一）；
   - docs/30 = v0.1.0 RC 发布说明与 17 条缺口（历史快照，不改）；
   - docs/34 = 主线归属与版本策略（规则层，本文引用它）；
   - docs/26 = 真实环境验收 Runbook（验收执行细节，本文 §7 引用它）。
4. 打 tag 后**复跑** `release-check` 一次，确认 tag↔版本一致（这是发版最后一步，也是唯一能证明 tag 正确的一步）。

---

## 9. 踩坑记录（本仓库已真实踩过，6 条）

| # | 坑 | 症状 | 修法 |
|---|---|---|---|
| 1 | PowerShell 传中文 commit message | 提交信息变乱码（`浠?agent-im...`） | 写 UTF-8 无 BOM 文件，`git commit -F <file>` |
| 2 | 文件 BOM | shebang 脚本 `node --check` 报 "Invalid or unexpected token" | 用 `UTF8Encoding($false)` 写；已加 BOM 的去掉首字符 |
| 3 | 本地代理中途失效 | `git push` 报 TLS `unexpected eof` / `Failed to connect ... proxy` | 直连与 socks 代理交替重试；先 `ls-remote` 探活 |
| 4 | Node 22 跑全量测试 | `node:sqlite` 默认关闭，测试 1 秒内失败 | CI 矩阵用 Node 24；README 写明 SQLite 需 24 |
| 5 | `raw.githubusercontent.com` 缓存 | main 的 README 显示旧内容，以为没推上去 | 用 commit SHA 的 raw URL 或 API contents 核对 |
| 6 | 迁移工具对象展开顺序 | `{ schemaVersion: NEW, ...data }` 旧值覆盖新值，1→2 迁移失败 | 改成 `{ ...data, schemaVersion: NEW }`；并补 1→2 用例 |

> 新增坑必须补进本表，否则下一个人会再踩一次。

---

## 10. 角色与红线

| 角色 | 职责 | 不能做 |
|---|---|---|
| 技术负责人（Agent） | 改代码/文档、跑门禁、提交、打 tag（用户批准后）、部署演练 | 批准 Stable；强推 `main`；把 mock 写成已接入 |
| 用户/产品负责人 | Stable 审批；真实资源提供；内测名单 | — |
| CI | 门禁执行者 | 不作为"人工签核"的替代（33 条业务 Gate 仍需人签） |

**红线（违反即回滚）**：

1. 未跑 `release-check` 就打 tag；
2. 版本号与 tag 不一致；
3. 带 schema 变更却没有迁移脚本/演练；
4. 把未验证能力（真实短信/实名/机审/支付、Flutter、Linux 真机）写成"已完成"；
5. 强推 `main` 或删除 `legacy/*` 历史。

---

## 11. 一页速查（发版当天按此执行）

```
[ ] 0   git status 干净、在 main、已同步
[ ] 1   改 version.mjs(VERSION/R#) + package.json + CHANGELOG + README 状态表
[ ] 2   node scripts/test.mjs && node scripts/verify.mjs
[ ] 3   node scripts/release-check.mjs        # 打 tag 前：tag 一致性可能 WARN/FAIL，属预期
[ ] 4   node scripts/release-check.mjs 前先提交；再打 tag；再复跑一次应 PASS
[ ] 5   五方核验（npm/Packages=N/A；Release 可选；CI 绿；资产 sha256；版本三元组）
[ ] 6   用户批准 Stable（只有用户能批）
[ ] 7   部署 + 预检；失败按 §7.2 回滚（代码 tag + 数据备份）
[ ] 8   归档 RELEASE-REPORT、更新 README、登记代替关系
```
