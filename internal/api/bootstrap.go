// 服务引导：构建存储 / 审批引擎 / Skill 运行时。
//
// 默认内存实现（离线自测/演示，零外部依赖）；
// 设置环境变量 PG_DSN（以及可选 PG_DRIVER，默认 pgx）时切换 PostgreSQL。
// 示例：
//
//	PG_DRIVER=pgx PG_DSN="postgres://user:pass@host:5432/haim?sslmode=disable" \
//	  go run ./services/agent-orchestrator
package api

import (
	"context"
	"database/sql"
	"log"
	"os"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/distill"
	"human-agent-im/internal/eventbus"
	"human-agent-im/internal/memory"
	"human-agent-im/internal/model"
	"human-agent-im/internal/msgseq"
	"human-agent-im/internal/skillruntime"
	"human-agent-im/internal/store"
	"human-agent-im/internal/stream"
)

// 付费消息单日发送上限（Phase 1 默认值）。
const defaultDailyLedgerLimit = 10

// NewDeps 构建服务依赖。driver/dsn 为空时使用内存实现。
func NewDeps(driver, dsn string) (*Deps, error) {
	var s *store.Store
	var db *sql.DB
	if dsn != "" {
		var err error
		db, err = store.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
		// v1.0.0 修复：启动时幂等执行内嵌迁移，新环境自动建表，杜绝"忘了手动 psql"。
		if err := store.Migrate(context.Background(), db); err != nil {
			return nil, err
		}
		// v2.3.27：用 messages 的实际最大序号播种进程级分配器（每 sender 各自 MAX+1）。
		// 必须在 Migrate 之后（依赖 messages 表），且失败不阻塞启动（降级为从 1 开始，
		// 由迁移 037 的唯一索引把重复暴露为显式错误而不是静默错序）。
		if n, err := msgseq.Default().SeedFromDB(context.Background(), db); err != nil {
			log.Printf("[bootstrap] WARN msgseq seed failed: %v", err)
		} else if n > 0 {
			log.Printf("[bootstrap] msgseq seeded %d senders from messages", n)
		}
		s = store.NewPostgresStore(db, defaultDailyLedgerLimit)
	} else {
		s = store.NewMemoryStore(defaultDailyLedgerLimit)
	}
	eng := approval.New(s.Approval)
	rt := skillruntime.NewRuntime(skillruntime.DefaultRegistry(), s.Capability)
	// v0.5.2：事件总线（审批/门控/扣费/记忆变更统一事件流）。
	bus := eventbus.New()
	streamHub := stream.NewHub()
	if db != nil {
		bus.SetPG(db)
	}
	// v1.0.0：Context 记忆——PG 可用时落 PG，否则回退内存。
	var memRaw memory.Store
	if db != nil {
		memRaw = memory.NewPGStore(db)
	} else {
		memRaw = memory.NewMemoryStore()
	}
	mem := memory.NewGuarded(memRaw)
	mem.SetBus(bus)
	// v0.7：蒸馏闭环（订阅记忆强化事件，自动收紧阈值）。
	distillEng := distill.New(bus)
	if db != nil {
		distillEng.SetPG(db)
	}
	// v0.8：L5 模型层（端侧桩 + 云端兼容，离线可跑）。
	edge := model.NewStub()
	cloud := model.NewOpenAICompatible(
		os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL"),
	)
	// v2.2.0：云端主/备双通道（DeepSeek 主，Kimi/Moonshot 备），主通道失败自动切备。
	var cloudChain model.Provider = cloud
	if os.Getenv("LLM_API_KEY") != "" && os.Getenv("LLM_FALLBACK_API_KEY") != "" {
		fb := model.NewOpenAICompatible(
			os.Getenv("LLM_FALLBACK_BASE_URL"),
			os.Getenv("LLM_FALLBACK_API_KEY"),
			os.Getenv("LLM_FALLBACK_MODEL"),
		)
		cloudChain = model.NewChain(cloud, fb)
	}
	// v1.3 重做：有 key 直接 cloud；无 key 回退 hybrid
	hybrid := model.NewHybrid(edge, cloudChain)
	var m model.Provider = hybrid
	if os.Getenv("LLM_API_KEY") != "" {
		m = cloudChain
	}
	hybrid.SetBus(&modelBusAdapter{bus: bus})
	return &Deps{Store: s, Skills: rt, Engine: eng, Memory: mem, Bus: bus, Distill: distillEng, Model: m, Stream: streamHub, DB: db}, nil
}

// NewDepsFromEnv 从环境变量读取 PG 配置（PG_DRIVER / PG_DSN）。
func NewDepsFromEnv() (*Deps, error) {
	dsn := os.Getenv("PG_DSN")
	driver := os.Getenv("PG_DRIVER")
	if dsn != "" && driver == "" {
		driver = "pgx"
	}
	return NewDeps(driver, dsn)
}
