// SPDX-License-Identifier: MIT
//
// 原生通道路由（C1 presence + C2 会话模型）。默认 NATIVE_ENABLED=0：**不注册任何路由**（404），
// 保证 embedded 生产路径与 golden 104 case 零变化。
//
// 可见性（WAVE-C-DESIGN §4.4）：
//   - 本人：聚合态 + 逐设备明细；
//   - L0–L2（self/family/trusted）：只给聚合态（不含设备列表/last_seen）；
//   - L3 known：只给 online/offline 布尔；
//   - L4 陌生/无关系/查询失败：404，**不泄露是否存在**（fail-closed）。
//
// 会话（C2）：
//   - 非成员读/写会话 → 404（fail-closed）；
//   - 成员上限超限 → 409 MEMBER_LIMIT_REACHED（事务内行锁校验并回滚）；
//   - 归档态 → 410 CONVERSATION_ARCHIVED；
//   - 所有错误统一 {error:{code,message}} 形状。
package routes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/native"
)

// C5b：灰度/回滚的可解释拒绝码（**不是 500，也不是静默 404**）。
const (
	// CodeNativeDisabled NATIVE_ENABLED=off：路由未装配，但返回明确拒绝码而非裸 404 文本。
	CodeNativeDisabled = "NATIVE_DISABLED"
	// CodeNativeNotRolledOut NATIVE_ENABLED=on 但调用者不在 NATIVE_ROLLOUT 白名单。
	CodeNativeNotRolledOut = "NATIVE_NOT_ROLLED_OUT"
)

var (
	nativeDisabledHits      atomic.Uint64 // /v1/native/* 在关闭态的命中次数（可观测）
	nativeRolloutDeniedHits atomic.Uint64 // 灰度名单拒绝次数（可观测）
)

// NativeDisabledHits 返回关闭态命中次数（诊断/测试用）。
func NativeDisabledHits() uint64 { return nativeDisabledHits.Load() }

// NativeRolloutDeniedHits 返回灰度拒绝次数（诊断/测试用）。
func NativeRolloutDeniedHits() uint64 { return nativeRolloutDeniedHits.Load() }

// nativeDisabledHandler 是 NATIVE_ENABLED=off 时挂在 /v1/native/ 的通配拒绝器：
// 仅影响 /v1/native/*（golden 104 case 不含这些路径，故默认关闭仍 0 diff）。
func nativeDisabledHandler(w http.ResponseWriter, r *http.Request) {
	nativeDisabledHits.Add(1)
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"code":    CodeNativeDisabled,
			"message": "native channel disabled (NATIVE_ENABLED=off); enable via NATIVE_ENABLED=on",
		},
	})
}

// nativeGuard 取 JWT 身份并做 NATIVE_ROLLOUT 灰度校验：
//   - 未配置灰度名单 → 放行；
//   - 在名单内（user/agent）→ 放行；
//   - 不在名单 → 421 NATIVE_NOT_ROLLED_OUT（可解释拒绝码，fail-closed 但不静默）。
func (c *routeCtx) nativeGuard(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := c.nativeIdentity(w, r)
	if !ok {
		return 0, false
	}
	rollout := c.kernel.Config.NativeRollout
	if rollout.Configured() {
		allowed, reason := rollout.Allows(r.Context(), c.deps.DB, uid, "")
		if !allowed {
			nativeRolloutDeniedHits.Add(1)
			writeJSON(w, http.StatusMisdirectedRequest, map[string]any{ // 421
				"error": map[string]any{
					"code":    CodeNativeNotRolledOut,
					"message": "caller not in NATIVE_ROLLOUT (" + reason + ")",
					"user_id": uid,
				},
			})
			return 0, false
		}
	}
	return uid, true
}

// registerNative 注册 /v1/native/* 路由（NATIVE_ENABLED=on 才注册）。
// presence 与 conversation 各自独立：有哪个装配就注册哪组。
func registerNative(r *Registry, c *routeCtx) {
	if c.kernel == nil || !c.kernel.Config.NativeEnabled {
		return
	}
	if c.kernel.NativePresence != nil {
		r.Add("GET", "/v1/native/presence/self", http.HandlerFunc(c.nativePresenceSelf), c.wrap)
		r.Add("GET", "/v1/native/presence/{userID}", http.HandlerFunc(c.nativePresenceByID), c.wrap)
	}
	if c.kernel.NativeConv != nil {
		registerNativeConversations(r, c)
	}
	if c.kernel.NativeMsg != nil {
		registerNativeMessages(r, c)
	}
	if c.kernel.NativeAgentTokens != nil {
		r.Add("POST", "/v1/native/agents/{id}/token", http.HandlerFunc(c.nativeIssueAgentToken), c.wrap)
		r.Add("DELETE", "/v1/native/agents/tokens/{jti}", http.HandlerFunc(c.nativeRevokeAgentToken), c.wrap)
	}
}

// ---------------- agent token（C4 最小闭环，不做 RBAC）----------------

func (c *routeCtx) nativeIssueAgentToken(w http.ResponseWriter, r *http.Request) {
	owner, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if agentID == "" {
		writeNativeErr(w, nativeErrInvalid("agent id required"))
		return
	}
	res, err := c.kernel.NativeAgentTokens.Issue(r.Context(), owner, agentID)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": res.Token, "jti": res.Jti, "typ": res.Typ,
		"agent_id": res.AgentID, "principal": res.Principal, "expires_at": res.ExpiresAt,
	})
}

func (c *routeCtx) nativeRevokeAgentToken(w http.ResponseWriter, r *http.Request) {
	owner, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	if err := c.kernel.NativeAgentTokens.Revoke(r.Context(), owner, r.PathValue("jti")); err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "jti": r.PathValue("jti")})
}

// ---------------- messages（C3）----------------

func registerNativeMessages(r *Registry, c *routeCtx) {
	r.Add("POST", "/v1/native/conversations/{id}/messages", http.HandlerFunc(c.nativeSendMessage), c.wrap)
	r.Add("GET", "/v1/native/conversations/{id}/messages", http.HandlerFunc(c.nativeListMessages), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/read", http.HandlerFunc(c.nativeMarkRead), c.wrap)
	r.Add("GET", "/v1/native/conversations/{id}/read", http.HandlerFunc(c.nativeReadCursor), c.wrap)
}

func (c *routeCtx) nativeSendMessage(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		ClientMsgID string `json:"client_msg_id"`
		Kind        string `json:"kind"`
		Body        string `json:"body"`
	}
	if err := decodeNativeBody(r, &req); err != nil {
		writeNativeErr(w, nativeErrInvalid("invalid json body"))
		return
	}
	view, dedup, err := c.kernel.NativeMsg.Send(r.Context(), native.SendRequest{
		ConversationID: r.PathValue("id"), SenderUserID: uid,
		ClientMsgID: req.ClientMsgID, Kind: req.Kind, Body: []byte(req.Body),
	})
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	status := http.StatusCreated
	if dedup {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"created":      !dedup,
		"deduplicated": dedup,
		"message":      view,
	})
}

func (c *routeCtx) nativeListMessages(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("afterBizSeq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := c.kernel.NativeMsg.List(r.Context(), r.PathValue("id"), uid, after, limit)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": list, "count": len(list)})
}

func (c *routeCtx) nativeMarkRead(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		BizSeq int64 `json:"biz_seq"`
	}
	if err := decodeNativeBody(r, &req); err != nil {
		writeNativeErr(w, nativeErrInvalid("invalid json body"))
		return
	}
	cur, err := c.kernel.NativeMsg.MarkRead(r.Context(), r.PathValue("id"), uid, req.BizSeq)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (c *routeCtx) nativeReadCursor(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	cur, err := c.kernel.NativeMsg.ReadCursor(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

// nativeIdentity 取 JWT 注入的 userID；没有则 401（native 路由一律要求可信身份）。
func (c *routeCtx) nativeIdentity(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := auth.UserIDFromContext(r)
	if !ok || uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "reason": "missing identity"})
		return 0, false
	}
	return uid, true
}

// writeNativeErr 把 native 具名错误映射为 HTTP status + code（未知错误 500，不泄漏细节）。
func writeNativeErr(w http.ResponseWriter, err error) {
	code := native.CodeOf(err)
	if code == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"code": "INTERNAL", "message": "internal error"},
		})
		return
	}
	writeJSON(w, native.StatusForCode(code), map[string]any{
		"error": map[string]any{"code": code, "message": err.Error()},
	})
}

// decodeNativeBody 解析 JSON 请求体。
func decodeNativeBody(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// nativeErrInvalid 构造参数错误。
func nativeErrInvalid(msg string) error {
	return &native.Error{Code: native.CodeInvalidArgument, Message: msg}
}

// ---------------- presence（C1）----------------

// nativePresenceSelf 返回本人的 presence（聚合态 + 逐设备明细）。
func (c *routeCtx) nativePresenceSelf(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	c.writePresenceSelf(w, uid)
}

// nativePresenceByID 查询指定 user 的 presence（按信任档位裁剪可见性）。
func (c *routeCtx) nativePresenceByID(w http.ResponseWriter, r *http.Request) {
	observer, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	subject, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil || subject <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if subject == observer {
		c.writePresenceSelf(w, subject)
		return
	}
	level := native.VisibilityLevel(r.Context(), c.deps.DB, observer, subject)
	switch level {
	case native.TrustStranger: // L4：404 fail-closed，不泄露存在性
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	case native.TrustKnown: // L3：只给 online/offline 布尔
		state, _ := c.kernel.NativePresence.Aggregate(subject)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": subject, "online": state == "online"})
	default: // L0–L2：只给聚合态
		state, _ := c.kernel.NativePresence.Aggregate(subject)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": subject, "state": state})
	}
}

// writePresenceSelf 输出本人 presence 明细（仅本人可见设备列表）。
func (c *routeCtx) writePresenceSelf(w http.ResponseWriter, uid int64) {
	state, devices := c.kernel.NativePresence.Aggregate(uid)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": uid, "state": state, "devices": devices,
		"device_list": c.kernel.NativePresence.Devices(uid),
	})
}

// ---------------- conversations（C2）----------------

func registerNativeConversations(r *Registry, c *routeCtx) {
	r.Add("POST", "/v1/native/conversations", http.HandlerFunc(c.nativeCreateConversation), c.wrap)
	r.Add("GET", "/v1/native/conversations", http.HandlerFunc(c.nativeListConversations), c.wrap)
	r.Add("GET", "/v1/native/conversations/{id}", http.HandlerFunc(c.nativeGetConversation), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/members", http.HandlerFunc(c.nativeAddMember), c.wrap)
	r.Add("DELETE", "/v1/native/conversations/{id}/members/{userID}", http.HandlerFunc(c.nativeRemoveMember), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/leave", http.HandlerFunc(c.nativeLeaveConversation), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/role", http.HandlerFunc(c.nativeChangeRole), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/transfer", http.HandlerFunc(c.nativeTransferOwner), c.wrap)
	r.Add("POST", "/v1/native/conversations/{id}/archive", http.HandlerFunc(c.nativeArchiveConversation), c.wrap)
}

func (c *routeCtx) nativeCreateConversation(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		Kind          string  `json:"kind"`
		Title         string  `json:"title"`
		ClientConvID  string  `json:"client_conv_id"`
		MemberUserIDs []int64 `json:"member_user_ids"`
		PaasConvID    string  `json:"paas_conv_id"`
	}
	if err := decodeNativeBody(r, &req); err != nil {
		writeNativeErr(w, nativeErrInvalid("invalid json body"))
		return
	}
	conv, created, err := c.kernel.NativeConv.Create(r.Context(), native.CreateRequest{
		OwnerUserID: uid, Kind: native.ConvKind(req.Kind), Title: req.Title,
		ClientConvID: req.ClientConvID, MemberUserIDs: req.MemberUserIDs, PaasConvID: req.PaasConvID,
	})
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "conversation": conv})
}

func (c *routeCtx) nativeListConversations(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	list, err := c.kernel.NativeConv.List(r.Context(), uid)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list, "count": len(list)})
}

func (c *routeCtx) nativeGetConversation(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	conv, err := c.kernel.NativeConv.Get(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (c *routeCtx) nativeAddMember(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID int64  `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := decodeNativeBody(r, &req); err != nil || req.UserID <= 0 {
		writeNativeErr(w, nativeErrInvalid("user_id required"))
		return
	}
	m, err := c.kernel.NativeConv.AddMember(r.Context(), r.PathValue("id"), uid, req.UserID, native.Role(req.Role))
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": m})
}

func (c *routeCtx) nativeRemoveMember(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	target, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil || target <= 0 {
		writeNativeErr(w, nativeErrInvalid("userID invalid"))
		return
	}
	if err := c.kernel.NativeConv.RemoveMember(r.Context(), r.PathValue("id"), uid, target); err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true, "user_id": target})
}

func (c *routeCtx) nativeLeaveConversation(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	if err := c.kernel.NativeConv.Leave(r.Context(), r.PathValue("id"), uid); err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"left": true})
}

func (c *routeCtx) nativeChangeRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID int64  `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := decodeNativeBody(r, &req); err != nil || req.UserID <= 0 {
		writeNativeErr(w, nativeErrInvalid("user_id required"))
		return
	}
	if err := c.kernel.NativeConv.ChangeRole(r.Context(), r.PathValue("id"), uid, req.UserID, native.Role(req.Role)); err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true, "user_id": req.UserID, "role": req.Role})
}

func (c *routeCtx) nativeTransferOwner(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		ToUserID int64 `json:"to_user_id"`
	}
	if err := decodeNativeBody(r, &req); err != nil || req.ToUserID <= 0 {
		writeNativeErr(w, nativeErrInvalid("to_user_id required"))
		return
	}
	if err := c.kernel.NativeConv.TransferOwner(r.Context(), r.PathValue("id"), uid, req.ToUserID); err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transferred": true, "to_user_id": req.ToUserID})
}

func (c *routeCtx) nativeArchiveConversation(w http.ResponseWriter, r *http.Request) {
	uid, ok := c.nativeGuard(w, r)
	if !ok {
		return
	}
	conv, err := c.kernel.NativeConv.Archive(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		writeNativeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}
