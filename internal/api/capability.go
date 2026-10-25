// v1.4.3: 代码层 Capability 门控（PreToolUse 强制）。
// Agent 调用工具前，必须先过这里检查信任档位和 capability 是否允许。
package api

import (
	"encoding/json"
	"net/http"
)

// GateRequest 门控请求。
type GateRequest struct {
	UserID      string `json:"userId"`
	TargetID    string `json:"targetId"`
	Tool        string `json:"tool"`        // 要调用的工具名
	Capability  string `json:"capability"`  // 需要的权限
	AmountCents int64  `json:"amountCents"` // 涉及金额（分）
}

// GateResponse 门控响应。
type GateResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Level   int    `json:"trustLevel"`
}

// toolTrustMap 工具→所需信任档位映射（代码层硬编码，非提示词）。
var toolTrustMap = map[string]int{
	"calendar.read":  2, // L2 信任以上可读日程
	"calendar.write": 1, // L1 家庭以上可写
	"msg.send":       2, // L2 以上可发消息
	"msg.read":       3, // L3 以上可读消息
	"payment.charge": 1, // L1 以上可扣费
	"contact.read":   3, // L3 以上可读联系人
	"contact.add":    2, // L2 以上可加联系人
}

func (d *Deps) gateCheck(w http.ResponseWriter, r *http.Request) {
	var req GateRequest
	json.NewDecoder(r.Body).Decode(&req)

	requiredLevel := toolTrustMap[req.Tool]
	if requiredLevel == 0 {
		requiredLevel = 4 // 未知工具默认 L4 陌生，最严
	}

	// 查 DB 里的实际信任档位
	actualLevel := 4 // 默认陌生
	if d.DB != nil && req.TargetID != "" {
		_ = d.DB.QueryRow(`SELECT trust_level FROM trust_relations WHERE owner_id=$1 AND contact_id=$2`,
			req.UserID, req.TargetID).Scan(&actualLevel)
	}

	// 门控：实际档位（数字越小越信任）必须 ≤ 要求档位
	if actualLevel >= requiredLevel {
		writeJSON(w, 200, GateResponse{
			Allowed: false,
			Reason:  "信任档位 L" + itoaCap(actualLevel) + " 不足，需要 L" + itoaCap(requiredLevel),
			Level:   actualLevel,
		})
		return
	}

	writeJSON(w, 200, GateResponse{Allowed: true, Level: actualLevel})
}

func itoaCap(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte(0 + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// GateMux 门控路由。
func (d *Deps) CapabilityMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/capability/check", d.gateCheck)
	return mux
}
