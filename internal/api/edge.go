package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"human-agent-im/internal/edge"
)

// ⚠️ 行为变更（v2.3.28，升级必读）：端侧出端白名单的默认值已从
// `["api.im2a.chat", "<已移除的用户生产地址>", "localhost"]`（且旧 EgressGuard 把空白名单
// 视为"不限制" → 未配置时实际上**全放行**）改为**默认空列表 + fail-closed（未配置时全拒绝）**。
//
// 升级影响：没有显式设置 `IM2A_EGRESS_ALLOW` 的部署，`/v1/edge/pipeline` 的出端裁决会从
// "允许"变为"拒绝"（`egress_allowed=false`，reason 含 `fail-closed`）。**这是有意的安全修复，
// 不是故障**：旧默认值等于默认允许外发，与"原始音频不出端"的产品承诺相反。
// 运维动作：升级前在 env 中配置 `IM2A_EGRESS_ALLOW`（格式见下方），否则端侧出站被拒。
// 详见 README §十一.3。

// v1.4 端侧智能接线。
//
// 说明（如实）：真机上端侧管道（唤醒/意图/敏感/出端裁决）应在 Flutter/端侧运行；
// 本服务端端点是开发联调替身，用同一套 internal/edge 逻辑跑，并把每次"出端裁决"
// 真实落 audit_events，从而让"音频不出端"成为可从数据库审计、而非口头声明的承诺。

// 端侧出端白名单：**必须显式配置，默认空 = fail-closed（一律拒绝）**。
//
// 合规（2026-10-25 用户硬指令）：源码里不得出现任何用户生产地址——云环境地址属于
// 部署配置，不属于源码。因此本默认值**不含任何 host**；部署时显式设置：
//
//	IM2A_EGRESS_ALLOW=api.example.com,localhost   # 逗号分隔的裸 host，不含 scheme
//
// 未配置时 /v1/edge/pipeline 的 egress_allowed 恒为 false（拒绝），这是有意的安全
// 默认值：不能让"忘了配置"变成"全部放行"。
//
// 格式校验（不静默接受垃圾配置）：
//   - 逗号分隔；每项 trim 首尾空白，空项直接忽略；
//   - 合法项 = **裸 host**（字母/数字/点/连字符/下划线，如 api.example.com、127.0.0.1），大小写不敏感；
//   - 带 scheme（https://）、路径（/）、端口（:）或非法字符的项 → **丢弃并打 WARN 日志**；
//   - 全部项都非法/为空 → 白名单为空 → fail-closed（拒绝出站），与未配置同义。
//
// 选 WARN+丢弃而非拒绝启动：本网关同时承载人类 IM 等业务，单个 env 拼写错误不应导致整机
// 不可用；但"被丢弃"是可观测的（启动即 WARN），且非法项绝不会进入放行集合。
func egressAllowHosts() []string {
	raw := strings.TrimSpace(os.Getenv("IM2A_EGRESS_ALLOW"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		host := strings.ToLower(strings.TrimSpace(p))
		if host == "" {
			continue // 空项（多余逗号/尾逗号）忽略：可预期，不算配置错误
		}
		if !egressHostRe.MatchString(host) {
			log.Printf("[edge] WARN: IM2A_EGRESS_ALLOW 忽略非法项 %q（要求裸 host，不含 scheme/路径/端口/空白）；该 host 不会获得出端许可", p)
			continue
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

// egressHostRe 是白名单项的合法形状：裸 host（不含 scheme/路径/端口），
// 覆盖常见主机名与 IPv4；IPv6 因 `hostOf` 的端口剥离逻辑无法安全匹配，显式拒绝（WARN）。
var egressHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// 端侧回连端点：默认由白名单第一个 host 派生（https://<host>/v1/intent），
// 可用 IM2A_EDGE_EGRESS_ENDPOINT 显式覆盖；白名单为空则返回空串。
// 该端点只用于构造待裁决 body / 记录审计，不会真的发起网络请求（Egress 只做字节裁决）。
func egressEndpoint(hosts []string) string {
	if ep := strings.TrimSpace(os.Getenv("IM2A_EDGE_EGRESS_ENDPOINT")); ep != "" {
		return ep
	}
	if len(hosts) == 0 {
		return ""
	}
	host := strings.TrimPrefix(strings.TrimPrefix(hosts[0], "https://"), "http://")
	return "https://" + host + "/v1/intent"
}

// newEdgePipeline 按当前环境构造端侧管道（测试可直接调用以验证 env 生效）。
func newEdgePipeline() *edge.Pipeline {
	hosts := egressAllowHosts()
	return edge.NewPipeline(hosts, egressEndpoint(hosts))
}

// 端侧允许回连的 host（与 EgressGuard 白名单一致）：运行期由 env 决定，代码中无默认地址。
var edgePipeline = newEdgePipeline()

type edgePipelineReq struct {
	Text string `json:"text"`
}

// EdgePipeline POST /v1/edge/pipeline
// 端侧文本进来：意图粗分类 → 敏感过滤 → 出端字节裁决 → 落 audit_events。
func (d *Deps) EdgePipeline(w http.ResponseWriter, r *http.Request) {
	var req edgePipelineReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Text == "" {
		writeErr(w, http.StatusBadRequest, "text is required")
		return
	}

	res := edgePipeline.Process(req.Text)
	allowed, reason, body := edgePipeline.Egress(res)

	decision := "allow"
	if !allowed {
		decision = "block"
	}
	detail, _ := json.Marshal(map[string]any{
		"category":       res.Category,
		"blocked":        res.Blocked,
		"hits":           res.Hits,
		"should_upload":  res.ShouldUpload,
		"egress_allowed": allowed,
		"reason":         reason,
		"redacted":       res.RedactedText,
		"body_bytes":     len(body),
	})
	if d.DB != nil {
		// 真实落库：每次端侧出端裁决都可审计
		if _, err := d.DB.Exec(
			`INSERT INTO audit_events(agent_id, action, decision, detail) VALUES($1,$2,$3,$4)`,
			"edge", "edge_egress", decision, detail,
		); err != nil {
			writeErr(w, http.StatusInternalServerError, "audit write failed: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"wakeup":         res.Wakeup,
		"category":       res.Category,
		"confidence":     res.Confidence,
		"blocked":        res.Blocked,
		"hits":           res.Hits,
		"should_upload":  res.ShouldUpload,
		"egress_allowed": allowed,
		"reason":         reason,
		"redacted_text":  res.RedactedText,
	})
}

// EgressReport GET /v1/edge/egress/report
// 直接从 audit_events 真实统计"音频不出端"的拦截情况。
func (d *Deps) EgressReport(w http.ResponseWriter, r *http.Request) {
	if d.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, "db not configured")
		return
	}
	var total, blocked int
	if err := d.DB.QueryRow(
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE decision='block')
		   FROM audit_events WHERE action='edge_egress'`,
	).Scan(&total, &blocked); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	violations := []map[string]any{}
	rows, err := d.DB.Query(
		`SELECT decision, detail, occurred_at
		   FROM audit_events WHERE action='edge_egress' AND decision='block'
		  ORDER BY occurred_at DESC LIMIT 20`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var dec, occ string
			var raw json.RawMessage
			_ = rows.Scan(&dec, &raw, &occ)
			violations = append(violations, map[string]any{
				"decision": dec, "detail": raw, "at": occ,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":      total,
		"blocked":    blocked,
		"violations": violations,
		"promise":    "every edge egress decision is persisted; raw audio bytes are rejected by EgressGuard before network.",
	})
}
