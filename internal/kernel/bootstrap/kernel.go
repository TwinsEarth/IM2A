// SPDX-License-Identifier: MIT
//
// Kernel 是网关运行期装配结果的持有者：依赖容器、插件宿主、可选 handler 与后台任务。
// main() 只负责 flag → Load → Assemble → routes.Register → ListenAndServe → 优雅关闭。
package bootstrap

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/agentregistry"
	"human-agent-im/internal/api"
	"human-agent-im/internal/botroute"
	"human-agent-im/internal/broadcast"
	"human-agent-im/internal/connapi"
	"human-agent-im/internal/inqueue"
	"human-agent-im/internal/kernel/native"
	"human-agent-im/internal/memory"
	"human-agent-im/internal/model"
	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/orchestration"
	"human-agent-im/internal/plugin"
	dsbotplugin "human-agent-im/internal/plugin/dsbot"
	openclawplugin "human-agent-im/internal/plugin/openclaw"
	"human-agent-im/internal/push"
	"human-agent-im/internal/relay"
	"human-agent-im/internal/skillbridge"
	"human-agent-im/internal/skillruntime"
	"human-agent-im/internal/tempgroup"
)

// Kernel 是网关装配结果。字段全部由 Assemble 填充；routes 包只读使用。
type Kernel struct {
	Config Config
	Deps   *api.Deps

	PluginHost     *plugin.Host
	OpenClawPlugin *openclawplugin.Plugin

	// OpenClawHealth 在账号目录可解析时非 nil（与重构前 main 内联变量一致）。
	OpenClawHealth *openclaw.HealthChecker

	// 以下 handler 仅在 deps.DB != nil 时装配（与重构前条件注册一致）。
	MCPHandler            http.Handler
	WechatWebhookHandler  http.Handler
	WechatAutoNegoHandler http.Handler

	// B2-c：事件镜像投递链路（standalone/dual 且 PG 可用时装配；embedded 恒 nil）。
	// ConnBridge/ConnSink/ConnTap 同时也是自检与测试的观测点。
	ConnBridge *connapi.Bridge
	ConnSink   *connapi.Remote
	ConnTap    *EventTap

	// C1：原生通道 presence 状态机（NATIVE_ENABLED=on 且 mirror 链路已装配时非 nil）。
	// presence 真相只在内存（TTL 45s/15s 刷新）；n_presence 只是 30s 快照。
	NativePresence *native.Presence

	// C2：原生会话模型（NATIVE_ENABLED=on 且 PG 可用时非 nil；embedded 默认 off → nil）。
	NativeConv *native.ConversationStore

	// C3：原生消息 + outbox（NATIVE_ENABLED=on 且 PG 可用时非 nil；有 bridge 才启投递 worker）。
	NativeMsg *native.MessageService

	// C4：agent token 签发/吊销（NATIVE_ENABLED=on 且 PG/JWT secret 可用时非 nil）。
	NativeAgentTokens *native.AgentTokenService

	// C4：topic→principal Resolver（观测/测试用；nil=未装配）。
	NativeResolver *native.Resolver
}

// Assemble 装配网关运行期依赖。语义与重构前 services/gateway/main.go 的内联装配逐段一致。
func Assemble(cfg Config) (*Kernel, error) {
	// B2-a（契约 v2 §1/§2）：CONN_MODE 校验前置——非法模式在装配任何依赖之前失败，
	// 错误信息必须带合法值列表（运维可自助纠正）。
	if !validConnMode(cfg.ConnMode) {
		return nil, fmt.Errorf("%w: %q (valid: %s)", ErrConnModeMisconfigured, cfg.ConnMode, strings.Join(ValidConnModes(), "|"))
	}
	switch cfg.ConnMode {
	case ConnModeStandalone:
		log.Printf("[conn] CONN_MODE=standalone: /v1/ws -> 421 + hint %q（WS 由 cmd/conn-gateway 承载；事件镜像装配见下方 [conn] 日志）", cfg.ConnHint)
	case ConnModeDual:
		log.Printf("[conn] CONN_MODE=dual: /v1/ws 保持 embedded 语义；事件镜像投递见下方 [conn] 装配日志")
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		return nil, err
	}
	k := &Kernel{Config: cfg, Deps: deps}

	// v2.3.26 OpenClaw 子插件接线（契约 PLUGIN-CONTRACT.md §1/§2/§9）：
	// Host 统一生命周期 + 归属公示。插件启动失败 / 降级只记录状态并继续服务，绝不让网关退出。
	pluginHost := plugin.NewHost(log.Default())
	ocPlugin := openclawplugin.New(openclawplugin.Deps{
		Logger:  log.Default(),
		Store:   deps.Store,
		DB:      deps.DB,
		Enabled: openclawPluginEnabled(),
	})
	if err := pluginHost.Register(ocPlugin); err != nil {
		log.Printf("[plugin] register %s failed: %v (gateway continues)", ocPlugin.ID(), err)
	}
	// v2.3.27 DeepSeek-Bot 子插件接线（契约 DSBOT-PLUGIN-CONTRACT.md）：
	// 移植 Web 搜索 / 长消息切分 / roster 只读规格；bridge 仅在 DSBOT_ENDPOINT 配置且健康时声明。
	dsPlugin := dsbotplugin.New(dsbotplugin.Deps{
		Logger:  log.Default(),
		Enabled: dsbotPluginEnabled(),
	})
	if err := pluginHost.Register(dsPlugin); err != nil {
		log.Printf("[plugin] register %s failed: %v (gateway continues)", dsPlugin.ID(), err)
	}
	// 用移植来的切分器替换微信出站"整段单条发送"（DeepSeek-Bot 子插件的实际替换点）：
	// 未注入时 openclaw 包行为与历史完全一致；上限可用 OPENCLAW_OUTBOUND_CHAR_LIMIT 覆盖。
	openclaw.TextSplitter = dsbotplugin.Split
	openclaw.OutboundCharLimit = openclawOutboundCharLimit()
	if err := pluginHost.Start(context.Background()); err != nil {
		log.Printf("[plugin] start failed: %v (gateway continues in degraded mode)", err)
	}
	// Deps 注入（§9-D3）：插件宿主供插件端点，发送器提供者供全仓出站统一（契约 §6）。
	deps.Plugins = pluginHost
	deps.OpenClawSender = ocPlugin.Sender
	printPluginBanner(pluginHost, ocPlugin)
	k.PluginHost = pluginHost
	k.OpenClawPlugin = ocPlugin

	// v2.1.2 OpenClaw 账号目录 watcher：新微信扫码后自动落库。
	// §9-D2：目录与回调 URL 均不再写死 —— 目录来自配置解析器，URL 从 -port 派生
	// （部署在 18080 时写死 8080 会让扫码回调打到错误端口，绑定永远不落库）。
	// 解析器可能返回 ""（HOME 不可解析）：此时降级为不监听，绝不监听相对路径。
	ocAccountsDir := openclaw.ResolveAccountsDir()
	if ocAccountsDir == "" {
		log.Printf("WARN: openclaw accounts dir unresolved (HOME/OPENCLAW_HOME/OPENCLAW_ACCOUNTS_DIR); account watcher/health disabled")
	} else {
		ocWatcher := openclaw.NewWatcher(ocAccountsDir, fmt.Sprintf("http://127.0.0.1:%d/v1/openclaw/auto-bind", cfg.Port))
		ocWatcher.Start()
		// A9 掉线/失效自动探测：账号文件消失 = 被踢/登录态失效，通知管理员并发"需重扫"给用户。
		ocHealth := openclaw.NewHealthChecker(ocAccountsDir, 30*time.Second, openclaw.AccountNotifier(healthNotifier{db: deps.DB, dir: ocAccountsDir, sender: ocPlugin.Sender}))
		ocHealth.Start()
		k.OpenClawHealth = ocHealth
	}

	// v2.2.0 微信 → openclaw → IM2A relay：MCP streamable-http 端点。
	// 仅本机 openclaw 调用（127.0.0.1），不经过公网/认证包装，也不暴露到 nginx。
	if deps.DB != nil {
		// §9-D2：账号目录统一来自配置解析器（watcher / health / relay 同一来源，不再写死）。
		ocResolver := relay.NewAccountResolver(ocAccountsDir)
		relaySvc := relay.New(deps.DB, ocResolver, deps.Model, botroute.New(deps.DB)).
			WithPeerSender(ocPlugin.Sender()).
			WithNegotiation(relay.NewWechatBridge(deps.Store.Negotiation).
				WithTempGroup(tempgroup.New(deps.DB)))
		// v2.2.2 全量广播：复用同一 openclaw 账号解析器与出站发送器（插件单例，契约 §6）。
		deps.Broadcast = broadcast.New(deps.DB, ocPlugin.Sender(), ocResolver)
		// B2：注入流式模型，微信自聊首块即时回发
		relaySvc.SetStreamer(model.NewOpenAICompatible(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")))
		k.MCPHandler = api.NewMCPHandler(relaySvc)
		// v2.2.1 标准 REST webhook（openclaw / 渠道确定性 POST），与 MCP 共用 relay 中枢。
		k.WechatWebhookHandler = api.NewWechatWebhookHandler(relaySvc, deps.DB, deps.OpenClawSender)

		// v2.3.1 A1：WEBHOOK_ASYNC=on 时启动入站队列 worker（PG OUTBOX，崩溃不丢）。
		if cfg.WebhookAsync && deps.DB != nil {
			// v2.3.19：L4 编排层真接线（默认 off，零影响）。
			// L4_ENABLED=on 时，自聊消息先过 Commander→Gate→Guard Runner；
			// 协商会话（NegoSessionID 非空）始终走旧链路，不被 L4 接管。
			selfHandler := inqueue.Handler(func(ctx context.Context, ev inqueue.Event) error {
				_, err := relaySvc.Relay(ctx, ev.Account, ev.FromWxid, ev.Text, "inq_"+ev.Account+"_"+ev.FromWxid+"_"+ev.Text)
				return err
			})
			// v2.3.25：Runner 注入真实 Skill 执行器（MCP 门控代理），审批放行后真正调 Skill，
			// 消除"假已办"；未接通的领域工具据实回告。
			skillRT := skillruntime.NewRuntime(skillruntime.DefaultRegistry(), nil)
			l4Runner := orchestration.NewRunner(
				orchestration.New(orchestration.NewFlashPlanner(
					model.NewOpenAICompatible(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")))),
				orchestration.DefaultGate(),
				orchestration.NewExitGuard(),
				orchestration.NewDistillStats(),
			).WithExecutor(skillbridge.New(skillRT))
			l4Hook := orchestration.NewL4Hook(l4Runner)
			// v2.3.23：L4 接入持久三层记忆（PG/pgvector，重启不丢、按 wxid 隔离）。
			if l4Hook != nil && deps.DB != nil {
				l4Hook = l4Hook.WithMemory(memory.NewPGStore(deps.DB), memory.NewPGSubjectResolver(deps.DB))
				log.Println("[inqueue] L4 three-layer memory wired (PG/pgvector)")
				l4Hook = l4Hook.WithTrust(agentregistry.New(deps.DB))
				log.Println("[inqueue] L4 real trust-level wired (agent_registry)")
			}
			ocSender := ocPlugin.Sender()
			selfHandler = inqueue.WithL4(selfHandler, l4Hook, func(ctx context.Context, ev inqueue.Event, reply string) error {
				_, err := ocSender.Send(ctx, ev.Account, ev.FromWxid, reply)
				return err
			})
			worker := inqueue.NewWorker(deps.DB, func(ctx context.Context, ev inqueue.Event) error {
				if ev.NegoSessionID != "" {
					_, err := relaySvc.NegoReply(ctx, ev.NegoSessionID, ev.FromWxid, ev.NegoIntent, ev.Text)
					return err
				}
				return selfHandler(ctx, ev)
			})
			go worker.Start(context.Background())
			if l4Hook != nil {
				log.Println("[inqueue] worker started (WEBHOOK_ASYNC=on, L4 orchestration ENABLED)")
			} else {
				log.Println("[inqueue] worker started (WEBHOOK_ASYNC=on, L4 off)")
			}
		}
		// v2.0.8 双 Agent 自动协商：双方 Agent 经 A2A 状态机多轮对谈，每轮真实推送给对方微信。
		k.WechatAutoNegoHandler = api.NewWechatAutoNegoHandler(relaySvc)
	}

	// v1.7.4：离线推送管理 API 客户端。
	deps.PushClient = &push.Client{BaseURL: cfg.PushBaseURL, SDKAppID: "1600168829", HTTP: http.DefaultClient}

	// v1.2.3：启动审批超时升级
	deps.StartApprovalEscalator()

	// C2：原生会话模型（NATIVE_ENABLED=on 且 PG 可用；默认 off → nil，零行为变化）。
	// 与 conn mode 无关：embedded 也可单独用 /v1/native/conversations 做会话管理（无投递）。
	if cfg.NativeEnabled && deps.DB != nil {
		k.NativeConv = native.NewConversationStore(deps.DB)
		log.Printf("[conn] NATIVE_ENABLED=on: 会话模型已装配 (limits: DIRECT=2 GROUP=12 TASK=8 AGENT_NEGOTIATION=4)")
	}

	// C4：agent token 服务（在 assembleConnMirror 之前装配：bridge 的 ConnOpen 校验要包吊销检查）。
	if cfg.NativeEnabled && deps.DB != nil && cfg.JWTSecret != "" {
		k.NativeAgentTokens = native.NewAgentTokenService(deps.DB, cfg.JWTSecret, cfg.NativeAgentTokenTTL)
		log.Printf("[conn] NATIVE_ENABLED=on: agent token 服务已装配 (iss=%s, ttl=%s, 可吊销)",
			native.AgentIssuer, cfg.NativeAgentTokenTTL)
	}

	// B2-c：standalone/dual 装配事件镜像投递链路（embedded 不装配，stream.Mirror 保持 nil）。
	// 装配顺序：先建 delivery store + connapi.Remote（UDS 后台异步连，不阻塞启动），
	// 再设置 stream.Mirror；关闭顺序见 Kernel.Close（先撤 mirror 再关投递）。
	k.assembleConnMirror(deps)

	// C3：原生消息服务 + outbox worker（必须在 bridge 装配之后，worker 要经 Bridge.Publish 投递）。
	// - embedded / 无 bridge：消息仍落库 + outbox pending（无投递链路，不启 worker），日志 WARN；
	// - standalone/dual：outbox worker 启动，投递复用 B2-b 的 im_delivery_log/位点。
	if cfg.NativeEnabled && deps.DB != nil {
		var pub native.MessagePublisher
		if k.ConnBridge != nil {
			pub = &bridgeMessagePublisher{bridge: k.ConnBridge, timeout: 5 * time.Second}
		}
		k.NativeMsg = native.NewMessageService(deps.DB, pub, log.Default())
		k.NativeMsg.StartOutbox(context.Background())
		if pub == nil {
			log.Printf("[conn] WARN: NATIVE_ENABLED=on 但无事件镜像/bridge（embedded 模式）：消息只落库+outbox pending，不投递（standalone/dual 才有投递链路）")
		} else {
			log.Printf("[conn] NATIVE_ENABLED=on: 消息 outbox worker 已启动 (bizSeq=会话内连续; 投递复用 B2-b im_delivery_log/位点)")
		}
	}

	return k, nil
}

// Close 优雅关闭：先撤事件 mirror（避免关机期间继续把事件送进将死的投递链路），
// 再停 EventTap/connapi.Remote，最后按插件宿主逆序停止插件。
func (k *Kernel) Close(ctx context.Context) error {
	if k == nil {
		return nil
	}
	if k.Deps != nil && k.Deps.Stream != nil {
		k.Deps.Stream.SetMirror(nil)
	}
	if k.ConnTap != nil {
		k.ConnTap.Close()
		k.ConnTap = nil
	}
	// C3：先停 outbox worker（它经 bridge 投递），再关投递出口/停 presence。
	if k.NativeMsg != nil {
		k.NativeMsg.StopOutbox()
		k.NativeMsg = nil
	}
	// C1：先停 presence 状态机（它会经 bridge 下发 ephemeral 帧），再关投递出口。
	if k.NativePresence != nil {
		k.NativePresence.Stop()
		k.NativePresence = nil
	}
	if k.ConnSink != nil {
		_ = k.ConnSink.Close(ctx)
		k.ConnSink = nil
	}
	if k.PluginHost == nil {
		return nil
	}
	return k.PluginHost.Stop(ctx)
}
