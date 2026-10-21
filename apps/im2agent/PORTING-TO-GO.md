# im2agent · Go 移植映射（本机基于 Node 的验证原型 → 你的 `cmd/` + `internal/` 结构）

> 本机没有 Go 工具链，所以这里给的是**逐文件、逐函数**的移植映射，而不是未经编译的 Go 代码。
> 原型的**行为**已被 10 项测试锁住；移植时把测试逐条翻成 Go 即可保证语义不漂移。

## 一、目录映射

| 你的 Go 结构 | 本仓库对应文件 | 说明 |
|---|---|---|
| `cmd/demo/main.go` | `apps/im2agent/server.mjs`（`PROFILE=demo` 分支）+ CLI 入口 | 单进程、内存存储、`go:embed web/*` ↔ 这里读 `public/` |
| `cmd/linux/main.go` | 同上（`PROFILE=lan`） | 差异只在绑定地址/打印 LAN 地址，不另起一套 |
| `cmd/prod/main.go` | 同上（`PROFILE=prod`） | 差异在强制 `ADMIN_TOKEN` + 安全头 + 存储选择 |
| `internal/server` | `apps/im2agent/server.mjs` 的 `route()` | 路由表完全一一对应（下节） |
| `internal/agent` | `domain.mjs` 的 `createWorld()` 里 `state.agents` | 原型只做注册表；Go 版应拆出 `agent.Registry` |
| `internal/approval` | `domain.mjs` 的 `decide()` | 状态机 `PENDING→APPROVED→EXECUTING→COMPLETED` |
| `internal/ledger` | 复用 `services/payment/src/ledger.mjs` | 幂等键 `pay:<msgId>`；Go 用 `(idempotency_key)` 唯一索引 |
| `internal/llm` | `apps/im2agent/llm.mjs` | 三档降级 `template → fallback → real`，纯 stdlib `net/http` |
| `internal/store` | `apps/im2agent/store.mjs` | 接口 `Read()/Write(snapshot)`；`InMem` / `JSONFile` / `PG` |
| `internal/web/admin` | `apps/im2agent/public/admin.html` | Go 用 `//go:embed admin.html` |
| `client/` | `client/` | Flutter，未编译 |
| `deploy/` | `deploy/{linux,lan,prod}` | systemd / compose / Caddy 已写好，可直接复用 |

## 二、数据结构（直接照抄即可）

```go
// internal/approval/ticket.go
type Ticket struct {
    ID        string    `json:"id"`
    AgentID   string    `json:"agent_id"`
    Title     string    `json:"title"`      // 例："陌生人来意审批"
    Context   string    `json:"context"`    // LLM 生成的摘要（原型：/api/approval/preview 写入）
    Price     int       `json:"price"`      // 分
    Status    string    `json:"status"`     // PENDING / APPROVED / EXECUTING / COMPLETED / REJECTED
    CreatedAt time.Time `json:"created_at"`
}

// internal/ledger/entry.go
type Entry struct {
    Time           time.Time `json:"time"`
    UserID         string    `json:"user_id"`
    Type           string    `json:"type"`   // CHARGE / REFUND / SETTLE
    Price          int       `json:"price"`
    Ref            string    `json:"ref"`    // msgId
    IdempotencyKey string    `json:"idempotency_key"`
}

// internal/store/store.go —— 三个实现
type Store interface {
    Read() ([]byte, error)   // 原型：整快照；PG 版建议按表读写
    Write([]byte) error
    Kind() string
}
```

## 三、路由映射（与原型逐条一致）

| 方法 | 路径 | 原型行为 | Go 落点 |
|---|---|---|---|
| GET | `/healthz` | 纯文本 `OK`（`Accept: application/json` 时回 JSON） | `internal/server` |
| GET | `/api/health` | JSON 健康 + 待审批 + 审计链 | 同上 |
| GET | `/api/events` | SSE 推送（`hello` / `preview` / `decide`） | `http.Flusher` + 客户端集合 |
| GET | `/api/state` | 对外状态 | 同上 |
| GET | `/api/llm` | LLM 档位/模型/失败数 | `internal/llm` |
| GET | `/api/admin/state` | 全量 + Go 视图（`ticketsView`/`audits`/`ledgers`） | `internal/server` |
| POST | `/api/approval/preview` | LLM 生成 `Ticket.Context` | `internal/llm` + `internal/approval` |
| POST | `/api/approval/decide` | 兼容 `{ticket_id, action\|decision}` | `internal/approval` |
| GET | `/api/ledger` · `/api/audit` · `/api/config` | 账本 / 审计链 / 运行配置 | 对应包 |

## 四、移植时的四个坑（原型已踩过）

1. **并发写 store**：原型是单线程 Node，天然原子；Go 必须给 store 加 `sync.RWMutex`，否则决策与快照会交错。
2. **`go:embed` 路径**：`//go:embed web/*` 只能嵌 `internal/web` 下的文件；原型 `public/` 要挪进去。
3. **时间与幂等**：账本幂等键必须是**数据库唯一索引**，不能只靠内存 map（重启即失效）。
4. **SSE 的 Flusher**：`http.ResponseWriter` 必须断言 `http.Flusher` 并逐事件 `Flush()`，否则客户端收不到（原型已用 `res.write` 逐条写）。

## 五、构建命令（目标机执行）

```bash
# ① demo：零依赖单二进制（内存）
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o im2agent-demo ./cmd/demo

# ③ linux：静态 ELF（体积取决于依赖；原型等价物约 12KB 的 JS，Go 版预计 7–12MB）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o im2agent ./cmd/linux

# ⑤ 局域网镜像：多阶段 → scratch/alpine
#   FROM golang:1.22 AS build && go build ... ; FROM gcr.io/distroless/static
#   期望 <10MB（纯 stdlib + 无 CGO 时可达）

# ⑦ 生产：读 .env + PG
CGO_ENABLED=0 go build -o im2agent-prod ./cmd/prod
```

## 六、验收口径（移植完成后逐条对照）

把 `tests/im2agent.test.mjs` 的 10 项用例翻成 `*_test.go`，逐条通过即视为语义等价：

```
① demo 连通 ② 运维鉴权+安全头 ③ prod 拒绝裸奔 ④ 审批闭环+账本幂等+终态保护
⑤ LLM 三档 ⑥ LAN 绑定 ⑦ jsonfile 重启恢复 ⑧/⑪ postgres 显式拒绝
⑨ Go 规范对齐（/healthz、snake_case、Go 视图、preview） ⑩ SSE 多端同步
```
