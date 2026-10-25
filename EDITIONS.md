# IM2A 版本模型（core 单一源 + 平台版本 edition 矩阵）

> **一句话**：IM2A 是**独立产品主线**；OpenClaw 频道插件只是**平台版本（edition）之一**
> （类比 iOS / Android / Windows 版本：共享同一套核心能力，各自独立版本与发布）。
>
> **版本号只改两处**：core 改 `internal/version/version.go`；edition 改 `editions.json`。
> 其余全部由 `make version-sync` 生成；CI 用 `make version-check` 防漂移。

---

## 1. 权威源

| 来源 | 内容 | 谁改 |
|---|---|---|
| `internal/version/version.go` | **core 版本唯一源**（`const Version = "vX.Y.Z"`），所有服务 `/healthz` 与 Release 资产共用 | 发布流程（打 tag 前统一收口） |
| `editions.json` | **各 edition 的独立版本 / 兼容 core 区间 / 产物路径 / 发布命令** | 各 edition 负责人 |

edition 版本**不得**硬编码进 core 的 `version.go`。

## 2. Edition 矩阵（生成表格见 README；本表说明形态）

| Edition | 形态 | 版本来源 | 产物 |
|---|---|---|---|
| `core`（服务端 Go） | 主产品 | `internal/version/version.go` | `services/gateway` 二进制 / Docker 镜像 |
| `client-ios` / `client-android` / `client-windows` | Flutter 客户端 | `editions.json`（各自独立版本） | 各平台安装包（`client/app`） |
| `openclaw-channel-relation` | **新增平台版本**（OpenClaw 频道插件，承载 IM2A 三样独占能力：关系档位矩阵 / 付费注意力状态机 / 三态 UI） | `editions.json`（独立 `0.x` + 声明兼容 core 区间） | npm / ClawHub 包（`packages/openclaw-channel-relation`） |

## 3. 生成与校验

```bash
make version-sync    # 由 version.go + editions.json 生成所有派生文件
make version-check   # 任一漂移 → 非 0 退出（CI 门禁）
```

`version-sync` 会同步（**不要手改这些文件里的版本号**）：

| 目标文件 | 取值来源 |
|---|---|
| `README.md` 的 core 版本徽章 + edition 表格 | core + `editions.json`（表格带 GENERATED 标记） |
| `npm/package.json`（core CLI 包） | core 版本 + `license: MIT` |
| `npm/bin/im2a.js` 的 `PKG_VERSION` | core 版本 |
| `client/app/pubspec.yaml` 的 `version` | 声明 `versionFile` 的 client edition |
| `deploy/Dockerfile` 的 `ARG VERSION` | **不设默认值**（构建时 `--build-arg VERSION=vX.Y.Z` 注入） |
| `packages/openclaw-channel-relation/package.json` 的 `version` | `openclaw-channel-relation` edition 版本 |
| `Makefile` 的 `VERSION` | `scripts/version.sh plain`（shell 读取，不硬编码） |

## 4. 发布规则

1. **core**：改 `internal/version/version.go` → `make version-sync` → `make version-check` → 打 tag → `make release-build`。
2. **edition**：改 `editions.json` 中该 edition 的 `version` → `make version-sync` → `make version-check` → 按 `publishCommand` 发布。
3. `compatibleCore` 必须是 `>=x.y.z <a.b.c` 形式，且**当前 core 版本必须落在区间内**（`make version-check` 会验证）。
4. edition 可以独立发版（如 `openclaw-channel-relation@0.1.x`），但不得要求超出其声明的 core 区间。

## 5. CI

`.github/workflows/ci.yml` 在 build 之前执行 `make version-check`；任何版本漂移会让 PR 红。
另有 `scripts/check-no-binaries.sh`（拒绝 >1MB 非文本文件）、LICENSE 字段检查、PG16 service 真库测试。
