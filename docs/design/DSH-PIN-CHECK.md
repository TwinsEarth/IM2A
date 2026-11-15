# DSH pin check

一条命令回答：**这台机器上装的 DSH，和插件的 `engines.dsh` 还对不对得上？**

```text
npm run pin:check
```

## 为什么需要它

「DSH 升级 → 插件 pin 过期 → 安装被拒」已经发生过三次（rc.1→rc.2、rc.2→rc.3 等）。
过去只有 `scripts/validate.mjs` 在**提交后**比对 `version-matrix.json`，装机当天仍要靠人记得改。

`pin:check` 把判断前移到升级当天：运行时一升级就报 `NEEDS_RELEASE`，并列出要改的四处。

## 判定与退出码

| 退出码 | 判定 | 含义 |
| --- | --- | --- |
| 0 | `OK` | 运行时 == 两份清单 pin == matrix target |
| 1 | `NEEDS_RELEASE` | 运行时变了，需要发新版（附建议版本号与待改文件） |
| 2 | `REPO_INCONSISTENT` | 运行时对得上，但清单/matrix 之间自相矛盾 |
| 3 | `UNKNOWN` | 读不到运行时版本，不算通过 |

退出码是分开的，方便 CI 或脚本区分「该发版」和「仓库自己乱了」。

## 运行时版本的三个来源

按成本从低到高，前一个失败才试下一个：

1. `--dsh-version x.y.z` 或环境变量 `DSH_VERSION`（显式覆盖，测试时用）
2. `dsh --version`
3. `<resources>/runtime/primary-runtime/runtime.json` 的 `desktopVersion`
   （由 `DSH_INSTALL_DIR` 反推 resources 目录，无需子进程）

都拿不到就是 `UNKNOWN`，**不会**默认返回 OK。

## 输出示例

```text
DSH pin check
  runtime (dsh --version) : 0.2.2-rc.3
  package.json engines.dsh : 0.2.2-rc.3   OK
  plugin.json  engines.dsh : 0.2.2-rc.3   OK
  version-matrix target    : 0.2.2-rc.3   OK
  plugin version           : 3.3.1

OK: runtime and repo records agree
```

漂移时：

```text
NEEDS_RELEASE: runtime 0.2.2-rc.4 does not match plugin pin 0.2.2-rc.3
  suggested version: 3.3.2
  edit:
    - packages/dsh-im2a-plugin/package.json  -> engines.dsh
    - packages/dsh-im2a-plugin/plugin.json   -> engines.dsh
    - version-matrix.json                   -> dsh-runtime.current/target
    - tests/v321-plugin-shell.test.mjs      -> RUNTIME_PIN
  then: node scripts/validate.mjs && npm run pin:check
```

`--json` 输出同样的结构，便于脚本消费。

## 设计取舍

- **判定逻辑与 IO 分离**：`evaluatePinState()` 是纯函数，`resolveDshVersion()` 可注入
  `run`/`env`，所以 8 条单测不需要真的装一个 DSH。
- **只读**：脚本不写任何文件，不改版本号，只报告。
- **建议版本只按 patch 递增**：`3.3.1 → 3.3.2`。如果这次升级同时改了工具面或契约，
  人应当按 semver 规则自行决定 minor，脚本不替你决定。
