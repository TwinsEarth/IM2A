// SPDX-License-Identifier: MIT
//
// B2-c：事件 tap + standalone/dual 装配。
//
// 背景（task-20）：内核业务事件此前只走 internal/stream.Hub.Broadcast（本地 WS 订阅者），
// 没有任何出口把它们送进 connapi。本文件把 stream.Hub 的可选 Mirror 钩子接到
// connapi.Bridge，形成「业务事件 → 发号/写日志（PG 同事务）→ UDS → conn-gateway → 客户端」链路。
//
// 主路径约束（task-20 验收 #4）：
//   - Mirror 回调在 Broadcast 调用方 goroutine 同步执行，因此 EventTap.Enqueue **只做非阻塞入队**
//     （有界队列，满则丢并计数），绝不做 DB/UDS IO；
//   - 发号+写日志（PG 事务）与 UDS 投递由单个 worker goroutine 完成，不拖慢 agent 主路径。
//
// 装配条件（task-20 §2）：
//   - CONN_MODE=embedded：**不装配**（stream.Mirror 保持 nil，Wave A 行为逐字节不变）；
//   - CONN_MODE=standalone/dual 且 PG_DSN 可用：装配 Bridge+Remote+EventTap 并设置 mirror；
//   - 无 PG：降级（WARN，端点语义仍可用，投递链路不装配）——持久位点/日志必须有 PG。
//   - UDS 不存在：Remote 后台退避重连，网关照常启动（不阻塞），投递降级计数增长。
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"human-agent-im/internal/api"
	"human-agent-im/internal/auth"
	"human-agent-im/internal/connapi"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/native"
)

// defaultTapQueue 事件 tap 的有界队列长度：>1000 足以吸收突发；满则丢并计数（可观测）。
const defaultTapQueue = 1024

// tapEvent 是进入投递链路的流事件快照。
// 约定：msg 在 Broadcast 返回后视为不可变（API 调用点均传一次性 map/list），
// worker 内再做 JSON 编码，避免把编码成本放到 Broadcast 主路径上。
type tapEvent struct {
	topic string
	msg   any
}

// EventTap 把 stream.Hub 的 Broadcast 事件非阻塞地转成 connapi 投递帧。
type EventTap struct {
	bridge *connapi.Bridge
	ch     chan tapEvent
	log    *log.Logger
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	published  atomic.Uint64 // 成功进入投递链路（Publish 无错）的事件数
	dropped    atomic.Uint64 // 队列满丢弃
	unmapped   atomic.Uint64 // topic 无法映射到 userID（计入 unmappedPrefixes 细分）
	marshalErr atomic.Uint64 // payload 编码失败

	// C4：Resolver（negotiation/subagent 多播）；nil 时退回纯前缀解析。
	resolver *native.Resolver

	// C4：unmapped 按 topic 前缀细分（指标 eventtap_unmapped_total{topic_prefix=...}）。
	prefixMu       sync.Mutex
	unmappedPrefix map[string]uint64
}

// newEventTap 启动 worker。bridge 为 nil 时仍可构造（Enqueue 安全丢弃），便于测试。
// resolver 可为 nil（退回 connapi 固定前缀解析；negotiation/subagent 计 unmapped）。
func newEventTap(bridge *connapi.Bridge, queue int, logger *log.Logger, resolver *native.Resolver) *EventTap {
	if queue <= 0 {
		queue = defaultTapQueue
	}
	if logger == nil {
		logger = log.Default()
	}
	t := &EventTap{
		bridge:         bridge,
		ch:             make(chan tapEvent, queue),
		log:            logger,
		closed:         make(chan struct{}),
		resolver:       resolver,
		unmappedPrefix: map[string]uint64{},
	}
	t.wg.Add(1)
	go t.run()
	return t
}

// Enqueue 是 stream.Hub.Mirror 的实现：**必须非阻塞**。
func (t *EventTap) Enqueue(topic string, msg any) {
	if t == nil {
		return
	}
	select {
	case <-t.closed:
		return
	default:
	}
	select {
	case t.ch <- tapEvent{topic: topic, msg: msg}:
	default:
		t.dropped.Add(1) // 队列满：丢事件并计数（事件本身非持久消息，可接受）
	}
}

// run 是唯一 worker：解析 topic→userID，编码 payload，调用 Bridge.Publish。
func (t *EventTap) run() {
	defer t.wg.Done()
	for {
		select {
		case <-t.closed:
			// 关机：把队列里已入队的事件尽快发完（最多一轮），不无限等待。
			for i := 0; i < cap(t.ch); i++ {
				select {
				case ev := <-t.ch:
					t.handle(ev)
				default:
					return
				}
			}
			return
		case ev := <-t.ch:
			t.handle(ev)
		}
	}
}

func (t *EventTap) handle(ev tapEvent) {
	// C4：Resolver 支持多播（negotiation → 双方 owner；subagent → owner），
	// unmapped 必须按 topic 前缀计数，不许静默丢。
	var users []int64
	mapped := false
	if t.resolver != nil {
		users, mapped = t.resolver.Resolve(context.Background(), ev.topic)
	} else if uid, ok := connapi.ResolveTopicUserID(ev.topic, nil); ok {
		users, mapped = []int64{uid}, true
	}
	if !mapped || len(users) == 0 {
		t.unmapped.Add(1)
		prefix := native.TopicPrefix(ev.topic)
		t.prefixMu.Lock()
		t.unmappedPrefix[prefix]++
		t.prefixMu.Unlock()
		return
	}
	payload, err := encodeTapPayload(ev.msg)
	if err != nil {
		t.marshalErr.Add(1)
		t.log.Printf("[conn] WARN: event payload encode failed topic=%s: %v", ev.topic, err)
		return
	}
	if t.bridge == nil {
		t.dropped.Add(1)
		return
	}
	critical := native.CriticalTopic(ev.topic)
	if os.Getenv("CONN_TAP_DEBUG") == "on" {
		t.log.Printf("[conn] TAP topic=%s users=%v critical=%v", ev.topic, users, critical)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	publishedAny := false
	for _, userID := range users {
		if _, err := t.bridge.Publish(ctx, userID, []connapi.PublishFrame{{
			Payload:  payload,
			Critical: critical, // C4：approval/trust/security 为 critical（慢消费者优先投递）
		}}); err != nil {
			t.log.Printf("[conn] WARN: event publish failed topic=%s user=%d: %v", ev.topic, userID, err)
			continue
		}
		publishedAny = true
	}
	if publishedAny {
		t.published.Add(1)
	}
}

// encodeTapPayload 把事件负载编码为投递帧 payload（网关不解析，客户端按事件类型消费）。
func encodeTapPayload(msg any) ([]byte, error) {
	switch v := msg.(type) {
	case nil:
		return []byte("null"), nil
	case []byte:
		return append([]byte(nil), v...), nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(msg)
	}
}

// Close 停止 worker（先撤 mirror 由调用方保证；这里只负责收尾）。
func (t *EventTap) Close() {
	if t == nil {
		return
	}
	t.once.Do(func() { close(t.closed) })
	t.wg.Wait()
}

// Stats 返回 tap 计数（published, dropped, unmapped, marshalErr）。
func (t *EventTap) Stats() (published, dropped, unmapped, marshalErr uint64) {
	if t == nil {
		return 0, 0, 0, 0
	}
	return t.published.Load(), t.dropped.Load(), t.unmapped.Load(), t.marshalErr.Load()
}

// UnmappedPrefixes 返回按 topic 前缀细分的 unmapped 计数（指标 eventtap_unmapped_total）。
func (t *EventTap) UnmappedPrefixes() map[string]uint64 {
	if t == nil {
		return nil
	}
	t.prefixMu.Lock()
	defer t.prefixMu.Unlock()
	out := make(map[string]uint64, len(t.unmappedPrefix))
	for k, v := range t.unmappedPrefix {
		out[k] = v
	}
	return out
}

// assembleConnMirror 在 standalone/dual 且 PG 可用时装配事件镜像投递链路。
// embedded 直接返回（stream.Mirror 保持 nil，生产行为零变化）。
func (k *Kernel) assembleConnMirror(deps *api.Deps) {
	if k.Config.ConnMode == ConnModeEmbedded {
		return
	}
	if deps.Stream == nil {
		log.Printf("[conn] WARN: CONN_MODE=%s 但 stream hub 不可用，事件镜像未装配", k.Config.ConnMode)
		return
	}
	if deps.DB == nil {
		log.Printf("[conn] WARN: CONN_MODE=%s 需要 PG_DSN（im_device_cursor/im_delivery_log 持久位点）；未配置 → 事件镜像降级关闭，仅保留端点语义", k.Config.ConnMode)
		return
	}
	store := delivery.NewPGStore(deps.DB, delivery.Options{Now: time.Now})
	bridge := connapi.NewBridge(store, log.Default())
	// C1：内核启用 JWT 权威身份校验（网关把 hello.token 透传到 ConnOpen.Token；
	// 内核验签后 ConnOpenAck.UserID 回显 JWT.sub，网关据此替换会话身份）。
	bridge.SetJWTSecret(k.Config.JWTSecret)
	remoteCfg := bridge.Wire(connapi.RemoteConfig{
		UDSPath:  k.Config.ConnUDSPath,
		Instance: fmt.Sprintf("im2a-%d", k.Config.Port),
		Logger:   log.Default(),
	})
	// C4：agent token 吊销校验（人类 token 放行；agent token iss=im2a-agent 查 n_agent_token）。
	// C5b：NATIVE_ROLLOUT 灰度也在此把关（连接级有 deviceID；HTTP 路由级在 routes_native）。
	// 不修改 connapi：包装 Wire 返回的 OnConnOpen（先验签+查吊销，再交给 Bridge 的权威身份逻辑）。
	if (k.NativeAgentTokens != nil && k.Config.JWTSecret != "") || k.Config.NativeRollout.Configured() {
		orig := remoteCfg.OnConnOpen
		tokens := k.NativeAgentTokens
		secret := k.Config.JWTSecret
		rollout := k.Config.NativeRollout
		remoteCfg.OnConnOpen = func(op connproto.ConnOpen) connproto.ConnOpenAck {
			if tokens != nil && op.Token != "" {
				if claims, err := auth.Parse(secret, op.Token); err == nil {
					if verr := tokens.ValidateClaims(context.Background(), claims); verr != nil {
						return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "token_revoked"}
					}
				}
			}
			ack := orig(op)
			if rollout.Configured() && ack.OK {
				uid := ack.UserID
				if uid <= 0 {
					uid = op.UserID
				}
				if allowed, _ := rollout.Allows(context.Background(), deps.DB, uid, op.DeviceID); !allowed {
					return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "native_not_rolled_out"}
				}
			}
			return ack
		}
	}
	remote := connapi.NewRemote(remoteCfg)
	bridge.SetSink(remote)
	// C4：topic→principal Resolver（negotiation 多播 / subagent owner；unmapped 指标化）。
	var resolver *native.Resolver
	if deps.Store != nil {
		resolver = native.NewResolver(deps.DB, deps.Store.Negotiation)
		k.NativeResolver = resolver
	}
	tap := newEventTap(bridge, defaultTapQueue, log.Default(), resolver)
	k.ConnBridge = bridge
	k.ConnSink = remote
	k.ConnTap = tap

	// C1：presence 状态机（NATIVE_ENABLED=on 才装配；默认 off → 零行为变化）。
	// 状态机只在内核：网关只上发 lease/connected/closed，offline 由 TTL 推断。
	if k.Config.NativeEnabled {
		ttl := k.Config.NativePresenceTTL
		if ttl <= 0 {
			ttl = native.DefaultPresenceTTL
		}
		presence := native.NewPresence(native.Options{
			TTL:      ttl,
			Tick:     k.Config.NativePresenceTick,
			Now:      time.Now,
			Logger:   log.Default(),
			Snapshot: native.NewPGStore(deps.DB),
			OnAggregate: func(st connproto.PresenceState) {
				// C4：优先 kind 27（dedicated 下行，网关只 fanout，不落库/不推位点）；
				// 未连接/Drain 或显式配置 ephemeral 时回退 TypePresence 数据帧（C1 行为）。
				if k.Config.NativePresenceDownlink == "state27" {
					if err := remote.DeliverPresenceState(st); err == nil {
						return
					}
				}
				payload, err := json.Marshal(st)
				if err != nil {
					return
				}
				bridge.DeliverEphemeral(st.UserID, []connproto.Frame{{
					Type: connproto.TypePresence, Payload: payload,
				}})
			},
		})
		presence.Start(context.Background())
		bridge.SetPresence(presence)
		k.NativePresence = presence
		log.Printf("[conn] NATIVE_ENABLED=on: presence 状态机已装配 (TTL=%s tick=%s 快照=30s；网关需 CONN_PRESENCE_LEASE=on)",
			presence.TTL(), presence.Tick())
	}

	deps.Stream.SetMirror(tap.Enqueue)
	log.Printf("[conn] %s 模式：事件镜像投递已装配 (CONN_UDS_PATH=%s, PG=on)；启动顺序=先连 UDS 后装 mirror，关闭顺序=先撤 mirror 再关投递",
		k.Config.ConnMode, k.Config.ConnUDSPath)
}
