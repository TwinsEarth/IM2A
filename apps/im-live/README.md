# AgentIM · 人机混合 IM 实时 Demo（apps/im-live）

> 本目录是「**可实机部署 & 运行 & 使用**」的聊天 Demo：浏览器里是 IM（会话 / 消息 / 审批卡），
> 背后复用本项目冻结的治理层（信任档位 / 审批状态机 / 付费消息 / 出口护栏），
> 并以 **DSH + W2M** 作为底层：relay 负责 presence 与任务派发，fleet 机器承担 Agent 执行。

## 1. 它演示什么（真实可操作的四个动作）

| 动作 | 在页面上怎么用 | 背后发生什么 |
|---|---|---|
| 熟人聊天 | 顶部切到「林总」，打开「家庭群 · 妈妈」，发一条消息 | 消息入库；Agent 参与（家庭 Agent 已先做过日程协调） |
| 陌生人过滤 | 切到「周先生」看「陌生人询盘」：**输入框是锁的** | 五档信任矩阵判定为 STRANGER；陌生人不能直接触达 |
| 审批决策 | 切回「林总」→ 右侧「待审批」→ 通过 | `approval-engine` 状态机：PENDING→EXECUTING→COMPLETED，扣费→查看→回复→结算，关系升级为 KNOWN，会话解锁 |
| 真实 fleet 取证 | 右侧「Fleet」→ 刷新 →（配置 token 后）在真实机器上执行一次 | `POST /v1/task` 派发到 relay，指定机器执行 `node --version`，轮询结果回显 |

治理自检（12 项可执行断言）在右侧面板实时显示：未授权 Skill、信任方向、金额阈值、预算耗尽、
先审后付、幂等扣费、拦截后不可付、协商轮次上限、身份证号拦截、记忆隔离、非法二次审批、授权过期。
**这些是代码断言，不是演示文案**。

## 2. 启动（零依赖，Node ≥ 22）

```powershell
cd E:\GPT\agent-im
node apps/im-live/server.mjs          # 默认 http://127.0.0.1:8797
# 或：PORT=9000 node apps/im-live/server.mjs
```

浏览器打开 `http://127.0.0.1:8797`，顶部切换身份：**林总 / 周先生 / 林母**。

- 种子场景：陌生人询盘审批卡 1 张 + 采购审批卡 1 张，`14:00` 前可直接点「通过 / 驳回」；
- 点「重置」回到种子场景（重启服务同样回到种子场景）。

### 测试与验收

```powershell
node scripts/test.mjs --filter=im-live   # 7 项 IM 端到端测试（聊天 / 审批 / 驳回 / fleet / 重置）
node scripts/test.mjs                    # 全量（180+ 项）
node scripts/verify.mjs                  # 文档 / 契约 / 代码一致性（含 openapi 路径↔实现）
```

## 3. 接入真实 DSH + W2M fleet（可选，但推荐）

默认 fleet 面板是**模拟态**（保证离线可跑、CI 可测）。要接真实 relay 与机器：

```powershell
$env:W2M_RELAY_URL      = 'http://202.182.123.154:8787'   # 或你自己的 relay
$env:W2M_OPERATOR_TOKEN = '<relay 的 operator-token.txt>'  # 派发任务需要
$env:W2M_DEVICE_TOKEN   = '<本机 device.json 里的 device_token>'  # 读执行结果需要（可省略）
node apps/im-live/server.mjs
```

配置后：

- `GET /api/fleet` 返回真实 relay `relay_id` 与在线机器列表（`/v1/fleet/members`）；
- `POST /api/fleet/probe { machineId }` 会真实派发一次任务并回显 stdout/exit；
- 页面右侧 Fleet 卡片显示 `mode=live`，并出现「在真实机器上执行一次」。

安全边界（必须遵守）：

- operator token 是**派发指令**的凭证，只放在服务端环境变量，绝不下发浏览器；
- 本 Demo 没有鉴权/多租户/HTTPS，仅用于**内网或本机演示**；对外暴露前必须加反向代理 + 登录 + TLS；
- 生产环境不要把 token 写进仓库或镜像层。

## 4. 实机部署（示例）

```bash
# 1) 代码放到服务器（Node ≥ 22，零依赖）
git clone <repo> /opt/agent-im && cd /opt/agent-im

# 2) systemd 单元：/etc/systemd/system/agent-im-demo.service
[Unit]
Description=AgentIM human-machine IM demo
After=network-online.target

[Service]
Type=simple
User=agentim
WorkingDirectory=/opt/agent-im
Environment=PORT=8797
Environment=W2M_RELAY_URL=http://202.182.123.154:8787
EnvironmentFile=/etc/agent-im/demo.env       # 放 W2M_OPERATOR_TOKEN / W2M_DEVICE_TOKEN
ExecStart=/usr/bin/node apps/im-live/server.mjs
Restart=always

[Install]
WantedBy=multi-user.target

# 3) 反向代理（Caddy 一行拿 TLS）：
#    demo.example.com { reverse_proxy 127.0.0.1:8797 }
```

健康检查：`GET /api/state` 返回 200 且 `checks.passed === 12`；`GET /api/fleet` 返回 `mode`。

## 5. API 一览（与 contracts/openapi 对齐）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/state?user=&thread=` | 会话列表 + 当前会话消息 + 待审批 + Agent 团队 + 信任矩阵 + 12 项自检 |
| POST | `/api/message` | `{ userId, threadId, text }`；信任档位门控（陌生人未审批 → 423/400 LOCKED） |
| POST | `/api/approvals/{id}/decide` | `{ action: APPROVE\|MODIFY\|REJECT }`；推进审批状态机与业务副作用 |
| GET | `/api/fleet` | relay 健康 + 在线设备（simulated / live） |
| POST | `/api/fleet/probe` | `{ machineId, argv? }` 在真实机器上执行一次并回显结果 |
| POST | `/api/reset` | 回到种子场景 |

## 6. 诚实的边界

- 会话/消息是**内存演示存储**，重启回到种子场景；生产消息通道见 docs/02（腾讯云 IM + 自有归档）。
- Agent 的「思考」由冻结的确定性治理模块执行（协商 / 预审 / 审批），**不调用大模型**；
  模型接入点见 docs/02 §Harness 映射，替换为真实 Agent Runtime 时不需要改本文档的协议与门控。
- fleet 面板的 live 模式依赖 relay 与本机 device token；relay 不可达时面板显示错误而**不会假装成功**。
- 未做：登录鉴权、多租户、消息持久化、离线推送、端侧硬件接入（见 docs/09 Phase 计划）。
