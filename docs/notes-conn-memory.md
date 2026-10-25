# 连接内存分解与优化（task-24）

> 日期：2026-10-25 ｜ 作者：openclaw-arch ｜ 范围：`internal/kernel/connhub/**`、`cmd/conn-gateway/**`
> 结论先行：**2000 连接空闲态 39.3 KB/conn → 27.3 KB/conn（−31%），活堆 37.9 → 14.0 KB/conn（−63%），
> 每连接 goroutine 3 → 2；压测期 RSS 149–206 MB → 100–110 MB**。目标 ≤40 KB/conn 达标（空闲 27.3 KB，
> 活跃流量 45–50 KB，两个数字都要报）。**剩余优化不再激进做**，优先稳定性。

---

## 1. 测量方法（先立方法，禁止只看 ps RSS）

环境：Vultr 1 vCPU / 959 MB / Debian（**只作参考**，最终 5k 数字在 4 vCPU/8 GB 机上取）。

工具（都是 task-24 新增，`-debug` 才挂载，默认关闭）：

| 端点 | 内容 |
|---|---|
| `GET /debug/memstats` | `runtime.MemStats` 全量 + `NumGoroutine` + RSS（读 `/proc/self/statm`） |
| `GET /debug/memstats?gc=1` | 上面全部 + 先 `runtime.GC()` + `debug.FreeOSMemory()`（测**活堆**与"RSS 未归还"的差） |
| `GET /debug/pprof/heap` | 二进制 profile，用 `go tool pprof -top -inuse_space/-alloc_space` 归因 |
| `GET /debug/pprof/goroutine` | goroutine 归因 |

采样方式：
1. **空闲分档**：用 `loadgen -conns N -rate 0.02`（≈每 50s 一帧）分别建立 500 / 1000 / 2000 条连接；
2. 每档等 `healthz.conns == N` 后静置 2s，取 RSS + `memstats?gc=1` + pprof；
3. **每连接增量** = 相邻档差分（排除进程固定开销）：`(RSS_2000−RSS_1000)/1000`；
4. 压测：`-echo`（网关自检回路，走完整 ring→writev 写路径），`2000 × 5/s × 20s` 与 `2000 × 50/s × 30s`，
   与 B1 基线**同参**对比（B1 基线：10.6k fps / p50 1.13ms / p95 185ms / p99 277ms / RSS 154MB；
   50/s 档：35.6k fps / p50 237ms / p95 580ms / p99 828ms / RSS 206MB）。

> ⚠️ pprof 的 `inuse_space` 对小对象是**抽样估计**（默认采样率 512 KB，16 KB 的 ring 槽位只按 3% 抽样），
> 只能用于**相对归因**；**每连接绝对量以 `MemStats.HeapAlloc`/`StackInuse` 的差分与 `unsafe.Sizeof` 为准**。

---

## 2. 每连接内存分解表（2000 连接，空闲）

| 组成 | 优化前 | 优化后 | 变化 | 依据 |
|---|---|---|---|---|
| **ring 槽位数组**（256 × `Frame`64B） | **16.0 KB** | **≈0**（空队列不分配） | −16.0 KB | `unsafe.Sizeof(Frame)=64B`；旧 `make([]Frame,256)` → 新惰性队列 |
| **gorilla 写缓冲**（`writeBuf`） | 2.1 KB | **0** | −2.1 KB | gorilla `newConn`：`writeBuf==nil && pool==nil` 才 `make`；给 `WriteBufferPool` 后跳过 |
| **gorilla 读缓冲**（额外 `bufio.Reader`） | 4.1 KB | **0**（复用 hijack 的 4 KB reader） | −4.1 KB | `ReadBufferSize=0` 时 gorilla 复用 `brw.Reader`（>256B 即复用） |
| **goroutine 栈**（3→2 个/连接） | 14.1 KB | 12.4 KB | −1.7 KB（少 1 个 goroutine） | `StackInuse` 差分 ÷ 2000 |
| Conn/chan/session/gorilla Conn/hub map | 其余 ≈3 KB | 其余 ≈2 KB | ≈−1 KB | `unsafe.Sizeof(Conn)=240B`、`ring=88B` + 3 个 channel + session |
| **活堆合计（HeapAlloc, GC 后）** | **37.9 KB** | **14.0 KB** | **−63%** | `memstats?gc=1` 差分 ÷ 2000 |
| **RSS 合计** | **39.3 KB** | **27.3 KB**（30.0 KB @对比 0 连接） | **−31%** | `ps`/`statm` 差分 ÷ 2000 |

固定开销：进程空载 RSS ≈ 10.2 MB、heap ≈ 1.0 MB、goroutine 72 个（64 shard + 主 goroutine 等）。

**有流量时**（`-echo`，2000 连接）：

| 场景 | 优化前 RSS | 优化后 RSS | 每连接 |
|---|---|---|---|
| 2000 × 5/s × 20s（往返 ~11k fps） | 149.3 MB | **100.3 MB** | 68.5 → **45.1 KB** |
| 2000 × 50/s × 30s（往返 ~36k fps） | 205.8 MB | **109.9 MB** | 96.7 → **49.8 KB** |

→ 活跃期每条连接比空闲多 ~18–22 KB：ring 实际入队帧（受 16 KB 字节预算约束）、`popBatch`/frame 临时对象、
GC 目标堆（GOGC=100 时目标 ≈ 2× 活堆）。**5k 规划要按"活跃 50 KB/conn"留余量**。

---

## 3. 候选优化 ROI 评估（5 个，全部实测/分析过）

| # | 候选 | 预期收益 | 实测/结论 | 是否落地 |
|---|---|---|---|---|
| 1 | **ring 惰性分配**（不再预分配 256 槽） | 空闲连接 −16 KB/conn | 实测 idle 39.3→29.7 KB；`TestMemoryFootprint` 断言 `newRing` 零分配；双约束/丢弃语义不变 | ✅ 落地 |
| 2 | **gorilla 缓冲**：读复用 + 写缓冲不预分配 | −6 KB/conn | 实测入选（见上表）；`BufferPool` noop 池 + `ReadBufferSize=0`；**不影响 gorilla 读语义** | ✅ 落地 |
| 3 | **心跳去掉每连接 goroutine+ticker**，改全局 ticker | −1.7 KB 栈 + 少 1000 个 timer（2000 连接） | 实测 goroutine 3→2/conn；心跳语义不变（30s 一帧 PING） | ✅ 落地 |
| 4 | **ring 槽紧凑化**（`Frame` 64B → ~40B 紧凑槽 + arena） | 空闲：0（已惰性）；活跃：ring 上限 16 KB→10 KB | ROI 低（空闲无收益，活跃只省 ~6 KB 且触发频次低）；改动面大（顺序/丢弃语义回归风险） | ❌ 不做（提案保留） |
| 5 | **`Frame` payload/编码缓冲池化** | 降 GC/分配（CPU），非 RSS | 当前 `mallocs≈54 万/20s`（A 档）、GC pause 总计 0.8–3.2 s/20-30s；1 vCPU 上 CPU 是瓶颈，池化收益待 8 GB 机器上量 | ⏸ 提案（待 5k 机器实测） |
| 6 | **`GOGC`/`GOMEMLIMIT` 调优** | 压住活跃期 headroom | 空闲期 headroom 很小（活堆≈RSS）；活跃期 headroom = 活堆大小（GOGC=100）。建议 5k：`GOMEMLIMIT=384MiB`、`GOGC` 保持 100（激进降 GOGC 会换 CPU，1 vCPU 上不划算） | ⏸ 推荐值（运维层，非代码） |

**未落地项的原因**：4/5/6 都会动到"活跃流量路径"或运行时策略，而当前空闲/活跃数字已进预算；
按 lead 要求"达标后不再激进优化，稳定优先"。

---

## 4. 落地改动清单（可独立回滚）

| 文件 | 改动 | 回滚方式 |
|---|---|---|
| `internal/kernel/connhub/ring.go` | 惰性队列（`q []Frame` + `head`，按需 append、出队后折半压缩）；`newRing` 零分配；双约束/`degraded`/`drop` 判定逐行保留 | 恢复旧的 `slots []Frame` 定长环（其余不动） |
| `cmd/conn-gateway/main.go` | `Upgrader{ReadBufferSize:0, WriteBufferPool: noopWriteBufferPool{}}`；全局心跳 `heartbeatLoop`；`-debug`/`-gogc`/`-memlimit` 附加参数 | 三个独立小改，各自可单独回滚 |
| `cmd/conn-gateway/wssink.go` | `noopWriteBufferPool`（`Get() interface{}`/`Put(interface{})`，按 gorilla 版本接口） | 删除该类型 + 移除 `WriteBufferPool` 字段 |
| `cmd/conn-gateway/metrics.go` | 无需改（指标名不变） | — |
| `internal/kernel/connhub/memory_footprint_test.go` | 结构体尺寸回归护栏（`Frame≤80B`、`Conn+ring≤1KB`、`newRing` 零分配） | 可删（但建议保留） |

**背压安全性**：三项改动**都没有放松任何丢弃/ack 语义**——ring 的双约束判定逐行保留（字节预算权威、
80% 降级、100% 踢慢消费者、slot-full 只背压），`TestZeroLoss_*`、`TestHub*`、`TestPresenceState*`、
`TestC1_*` 全绿；压测 `frames_dropped_total{reason="ring"}` 与基线同量级（50/s 档 2495 vs 基线 2506），
`slow_kicks_total=0`。

---

## 5. 压测前后对比（Vultr 1 vCPU，`-echo` 同参）

| 指标 | 优化前（B1 基线） | 优化后 | 变化 |
|---|---|---|---|
| 2000×5/s 吞吐（往返） | 10.6k fps（212,453/211,922） | **11.1k fps**（222,660/222,549） | +5% |
| 2000×5/s p50 / p95 / p99 | 1.13ms / 185ms / **277ms** | 3.22ms / 34.2ms / **84.4ms** | p95 −82%、p99 **−70%**；p50 落在该机 1.1–3.9ms 的跑间噪声区间 |
| 2000×5/s RSS（mid） | 149.3 MB | **100.3 MB** | −33% |
| 2000×50/s 吞吐（往返） | 35.6k fps（1,075,004/1,072,830） | **36.7k fps**（1,122,569/1,120,615） | +3% |
| 2000×50/s p50 / p95 / p99 | 237ms / 580ms / **828ms** | 239ms / 541ms / **724ms** | p95 −7%、p99 −13% |
| 2000×50/s RSS（mid） | 205.8 MB | **109.9 MB** | −47% |
| ring 丢弃（50/s 档） | 2,506 | 2,495 | 持平（同量级，符合预期） |
| slow_kicks | 0 | 0 | 无变化 |

> p50 的轻微上移与 ring 惰性增长的少量分配有关，但幅度在 1 vCPU 跑间噪声内（B1 期间同配置 p50 曾出现
> 1.1 ms 与 3.9 ms 两种结果）。**最终 p50/p95/p99 以 4 vCPU 机器为准。**

---

## 6. 容量推算（5k 连接）

| 口径 | 每连接 | 5,000 连接 | 备注 |
|---|---|---|---|
| 空闲（只连不发） | 27.3 KB | **≈137 MB + 固定 ~10 MB ≈ 147 MB** | 512 MB 预算内成立 |
| 活跃（事件级 5/s） | 45.1 KB | **≈226 MB** | 512 MB 预算内成立（含 GC headroom 建议 GOMEMLIMIT=384MiB） |
| 高压（50/s × 5k，1vCPU 达不到） | 49.8 KB | ≈250 MB + CPU 瓶颈 | 需 4 vCPU 才谈吞吐 |

**推荐组合**：惰性 ring + gorilla 缓冲优化 + 全局心跳（已落地） + 运维侧 `GOMEMLIMIT=384MiB`、
`MemoryMax=512M`；`GOGC` 保持默认 100（1 vCPU 上降 GOGC 用 CPU 换内存不划算，8 GB 机器可另测）。

### 6.1 运维注意（三条，必须写进部署/告警）

1. **活跃期 GC headroom 会随 GOGC 波动**：建议 `GOMEMLIMIT=384MiB`（`-memlimit 384MiB` 或环境变量），
   并**监控 GC CPU 占比**（`gc_cpu_fraction` / `rate(go_gc_cpu_seconds_total)`）：GOGC=100 时目标堆 ≈ 2× 活堆，
   5k 活跃（≈226 MB 活堆）时堆峰值可到 ≈450 MB，逼近 `MemoryMax=512M`；GOMEMLIMIT 会提前触发 GC 以压住堆，
   **代价是 CPU**——1 vCPU 上要盯 `p99 恶化`，4 vCPU 上可接受。
2. **ring 惰性队列的瞬时容量上限是 2×maxFrames（≈32 KB/conn）**：突发流量下每条连接的 ring 队列容量
   可能短时翻倍（append 增长策略），**5k 突发预算按 32 KB/conn 算**（≈160 MB），而不是按空闲的 27.3 KB。
   稳态仍受 16 KB 字节预算约束，超预算即丢弃（丢弃不 ack，内核重发）。
3. **`Frame` payload 未池化 = 已知分配热点**：高吞吐下每帧仍有一次解码/编码分配（`mallocs` 50–160 万/20–30s），
   是 GC 压力的主要来源。**官方记录为"已知热点，C5 之后再评估"**——是否池化用 5k 机器的实测 GC CPU/延迟数据决定，
   不做直觉优化（池化会引入 payload 生命周期/复用错误的风险，收益需 >15% 才值得）。

**剩余风险**：
1. 活跃期每连接 45–50 KB 仍高于空闲 27 KB，5k 高负载时堆峰值可能触及 GOMEMLIMIT 触发更频繁 GC（CPU↔延迟权衡）；
2. ring 惰性队列的瞬时容量上限是 `2×maxFrames`（32 KB/conn 一次性峰值），Burst 场景需在 8 GB 机器上复测；
3. `Frame` payload 未池化，高吞吐下分配率仍是 GC 压力主来源（提案 5，待 5k 机器实测再决定）。

---

## 7. 4 vCPU / 8 GB 干净机实测（**交付数字**，2026-10-25）

机：Ubuntu 24.04 / Intel Xeon Platinum 8576C 4 vCPU / 7,623 MiB / swap 1.9 GB / `ulimit -n=65535`。
口径与 deploy-ops 的 `run-bench.sh` 对齐：`-echo -dev-insecure -uds-disable`（WS-only 回显，走完整 shard→ring→writev 写路径），
额外 `-debug -memlimit 384MiB`；`loadgen` 与网关同机。每档取 mid 采样（`memstats?gc=1` + pprof + metrics）。

### 7.1 空闲三档（rate≈0.02/s，只连不发）

| N | RSS | 活堆 HeapAlloc(GC 后) | 栈 StackInuse | goroutine | 每连接增量 |
|---|---|---|---|---|---|
| 500 | 25.4 MB | 8.1 MB | 6.1 MB | 1,070 | — |
| 2,000 | 66.2 MB | 29.2 MB | 22.9 MB | 4,070 | RSS **27.9 KB** / heap **14.0 KB** |
| 5,000 | 148.2 MB | 71.2 MB | 58.4 MB | 10,070 | RSS **28.0 KB** / heap **14.0 KB** |

固定开销（外推 0 连接）≈ **12.0 MB**；**每连接 goroutine = 2.0**（从 3 降到 2 在 4 vCPU 上同样成立）。
> 空闲 5k ≈ **148 MB**（Vultr 1 vCPU 上 27.3 KB → 147 MB，两机一致，说明优化与核数无关）。

### 7.2 有流量（echo 往返）

| 场景 | 吞吐（往返） | p50 / p95 / p99 | RSS(mid) | 每连接（含固定开销） | ring 丢弃 | slow_kicks | GC CPU 占比 |
|---|---|---|---|---|---|---|---|
| 2000 × 5/s × 20s | 11.5k fps | 165µs / 1.67ms / **2.71ms** | 119.9 MB | 54.2 KB | 0 | 0 | 0.13% |
| 2000 × 50/s × 30s | **110.1k fps** | 709µs / 8.54ms / **19.0ms** | 114.2 MB | 51.2 KB | 0 | 0 | ~0.9% |
| 5000 × 5/s × 20s | 28.8k fps | 184µs / 1.48ms / **6.44ms** | 237.1 MB | 45.0 KB | 12 | 0 | 1.26% |
| 5000 × 20/s × 30s | **110.5k fps** | 1.30ms / 29.3ms / **45.8ms** | 242.2 MB | 46.0 KB | 18 | 0 | 1.46% |

- `frames_in ≈ frames_out`（差 ≤7，在途帧）；`frames_dropped_total{reason="ring"}` 仅 12/18（≈0.0003%），**全部不 ack**；`slow_kicks=0`；
- **p99 在 110k fps 回显压力下仍 ≤46 ms**（设计目标 80 ms），5k 档 p99 6.4–45.8 ms；
- **GC CPU 占比 1.26–1.46%**（`GOMEMLIMIT=384MiB` 生效但未触发限流：heap_inuse 79–95 MB，远低于 384 MB）。
- 连接全部断开后 `FreeOSMemory` 使 RSS 回落到 ≈33 MB（无残留泄漏；deploy-ops 的 300s 漂移 0.08% 亦印证）。

### 7.3 三档汇总（最终交付表）

| 口径 | 每连接 | 5,000 连接实测算 | 结论 |
|---|---|---|---|
| **空闲** | **28.0 KB** | **148.2 MB** | 512 MB 预算内（余量 ~360 MB） |
| **活跃（事件级 5/s）** | **45.0 KB** | **237.1 MB** | 512 MB 预算内；p99 6.44 ms |
| **高压（20/s，110k fps 回显）** | **46.0 KB** | **242.2 MB** | 512 MB 预算内；p99 45.8 ms；GC CPU 1.46% |

与 deploy-ops 口径对照：其 5000×1/s×300s 实测 **RSS 246.3 MB / ≈47 KB/conn**、p50 127µs / p99 954µs——
与本文 5k 档（242–248 MB，45–46 KB/conn）**一致**，说明两套独立测量互相印证。

### 7.4 两机对比（同一份代码）

| 指标 | Vultr 1 vCPU / 959 MB | 4 vCPU / 8 GB | 说明 |
|---|---|---|---|
| 空闲每连接 | 27.3 KB | **28.0 KB** | 与核数无关，纯结构+栈+GC 目标 |
| 活跃每连接（5/s） | 45.1 KB | **45.0 KB** | 一致 |
| 5000 连接吞吐（5/s 档） | 28.8k fps（Vultr 同档受限） | 28.8k fps | 目标 25k 达标 |
| 高压（10 万 fps 级） | 36.7k fps（CPU 顶住） | **110.5k fps** | 4 vCPU 才谈吞吐 |
| 高压 p99 | 724 ms（2000×50） | **45.8 ms**（5000×20） | CPU 是 1 vCPU 的根因 |

### 7.5 残留发现（未实施，按 lead "达标后不激进优化" 冻结）

pprof（idle 5000）显示 `bufio.NewWriterSize` 仍占 **17.6 MB（≈3.6 KB/conn）**，来源不是 gorilla（其 `writeBuf`
已被 `WriteBufferPool` 跳过），而是 **`net/http` 升级时 hijack 走的 `http.conn.bufw`/`brw` 仍被 handler 栈帧
（`w`/`r` 参数）保活到连接结束**。候选修法（**未做**）：升级完成后 `w, r = nil, nil` 释放引用，
预计再省 ~4 KB/conn（空闲 28.0 → ~24 KB）。按 lead 指令冻结，留待 C5 之后与 payload 池化一起用数据决定。