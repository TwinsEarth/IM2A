// v1.7.10 内容审核：规则落库的自动扫描 + 人工审核队列。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 规则类型
const (
	ruleKeyword = 1
	rulePhone   = 2
	ruleURL     = 3
	ruleRegex   = 4
)

// 规则动作
const (
	ruleActionBlock  = 1
	ruleActionReview = 2
)

// 自动判定
const (
	autoPending = 0
	autoAllowed = 1
	autoBlocked = 2
)

// 人工状态
const (
	manualPending = 0
	manualAllow   = 1
	manualBlock   = 2
	manualFalse   = 3
)

var (
	modPhoneRe = regexp.MustCompile(`1[3-9]\d{9}`)
	modURLRe   = regexp.MustCompile(`https?://`)
)

type modRule struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	RuleType int    `json:"rule_type"`
	Pattern  string `json:"pattern"`
	Action   int    `json:"action"`
	Enabled  int    `json:"enabled"`
}

func modRuleMatches(rt int, pattern, content string) bool {
	switch rt {
	case ruleKeyword:
		return strings.Contains(strings.ToLower(content), strings.ToLower(pattern))
	case rulePhone:
		return modPhoneRe.MatchString(content)
	case ruleURL:
		return modURLRe.MatchString(strings.ToLower(content))
	case ruleRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(content)
	}
	return false
}

func (d *Deps) loadEnabledRules(ctx context.Context) ([]modRule, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT id,code,rule_type,pattern,action,enabled FROM content_rules WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []modRule{}
	for rows.Next() {
		var r modRule
		if err := rows.Scan(&r.ID, &r.Code, &r.RuleType, &r.Pattern, &r.Action, &r.Enabled); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// 业务侧扫描（普通 token）。
func (d *Deps) ModerationMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/moderation/scan", d.modScan)
	return mux
}

type modScanReq struct {
	Ref     string `json:"ref"`
	Source  string `json:"source"`
	Content string `json:"content"`
}

func (d *Deps) modScan(w http.ResponseWriter, r *http.Request) {
	var req modScanReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, 400, "content required")
		return
	}
	if req.Source == "" {
		req.Source = "msg"
	}
	rules, err := d.loadEnabledRules(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var blocked, review []string
	for _, rl := range rules {
		if modRuleMatches(rl.RuleType, rl.Pattern, req.Content) {
			hit := rl.Code
			if rl.Pattern != "" {
				hit = rl.Code + ":" + rl.Pattern
			}
			if rl.Action == ruleActionBlock {
				blocked = append(blocked, hit)
			} else {
				review = append(review, hit)
			}
		}
	}
	decision := "allowed"
	auto := autoAllowed
	hits := []string{}
	if len(blocked) > 0 {
		decision = "blocked"
		auto = autoBlocked
		hits = blocked
	} else if len(review) > 0 {
		decision = "review"
		auto = autoPending
		hits = review
	}
	hitsJSON, _ := json.Marshal(hits)
	detailJSON, _ := json.Marshal(map[string]any{"source": req.Source, "hits": hits})

	var queueID int64
	if decision != "allowed" {
		if err := d.DB.QueryRowContext(r.Context(),
			`INSERT INTO content_moderation_queue(ref,source,content,auto_decision,hit_rules)
			 VALUES($1,$2,$3,$4,$5::jsonb) RETURNING id`,
			req.Ref, req.Source, req.Content, auto, string(hitsJSON)).Scan(&queueID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	action, dec := "CONTENT_ALLOWED", "ok"
	if decision == "blocked" {
		action, dec = "CONTENT_BLOCKED", "block"
	} else if decision == "review" {
		action, dec = "CONTENT_REVIEW", "review"
	}
	d.DB.ExecContext(r.Context(),
		`INSERT INTO audit_events(occurred_at,trace_id,message_id,action,decision,detail)
		 VALUES(now(),'',$1,$2,$3,$4::jsonb)`, req.Ref, action, dec, string(detailJSON))

	writeJSON(w, 200, map[string]any{"status": "ok", "decision": decision, "hits": hits, "queue_id": queueID})
}

// admin 侧
type modQueueItem struct {
	ID           int64           `json:"id"`
	Ref          string          `json:"ref"`
	Source       string          `json:"source"`
	Content      string          `json:"content"`
	AutoDecision int             `json:"auto_decision"`
	HitRules     json.RawMessage `json:"hit_rules"`
	ManualStatus int             `json:"manual_status"`
	Reviewer     string          `json:"reviewer"`
	ReviewNote   string          `json:"review_note"`
	CreatedAt    string          `json:"created_at"`
	ReviewedAt   string          `json:"reviewed_at"`
}

const modQueueCols = `id,ref,source,content,auto_decision,hit_rules,manual_status,reviewer,review_note,COALESCE(created_at::text,''),COALESCE(reviewed_at::text,'')`

func scanModQueue(s interface{ Scan(...any) error }) (*modQueueItem, error) {
	var q modQueueItem
	if err := s.Scan(&q.ID, &q.Ref, &q.Source, &q.Content, &q.AutoDecision, &q.HitRules, &q.ManualStatus, &q.Reviewer, &q.ReviewNote, &q.CreatedAt, &q.ReviewedAt); err != nil {
		return nil, err
	}
	return &q, nil
}

func (d *Deps) moderationAdminRegister(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/moderation/queue", d.adminModQueue)
	mux.HandleFunc("POST /v1/admin/moderation/{id}/review", d.adminModReview)
	mux.HandleFunc("GET /v1/admin/moderation/rules", d.adminModRulesList)
	mux.HandleFunc("POST /v1/admin/moderation/rules", d.adminModRuleCreate)
	mux.HandleFunc("POST /v1/admin/moderation/rules/{id}/toggle", d.adminModRuleToggle)
}

func (d *Deps) adminModQueue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 50)
	if limit < 1 || limit > 300 {
		limit = 50
	}
	where := " WHERE 1=1"
	args := []any{}
	if s := q.Get("status"); s != "" {
		args = append(args, s)
		where += " AND manual_status=$" + strconv.Itoa(len(args))
	}
	if a := q.Get("auto"); a != "" {
		args = append(args, a)
		where += " AND auto_decision=$" + strconv.Itoa(len(args))
	}
	args = append(args, limit)
	rows, err := d.DB.QueryContext(r.Context(), `SELECT `+modQueueCols+` FROM content_moderation_queue`+where+` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []modQueueItem{}
	for rows.Next() {
		if it, err := scanModQueue(rows); err == nil {
			out = append(out, *it)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "total": len(out), "items": out})
}

func (d *Deps) adminModReview(w http.ResponseWriter, r *http.Request) {
	id := atoiDefault(r.PathValue("id"), 0)
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
		Reviewer string `json:"reviewer"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	var ms int
	switch body.Decision {
	case "allow":
		ms = manualAllow
	case "block":
		ms = manualBlock
	case "false_positive":
		ms = manualFalse
	default:
		writeErr(w, 400, "decision must be allow|block|false_positive")
		return
	}
	if body.Reviewer == "" {
		body.Reviewer = "admin"
	}
	row := d.DB.QueryRowContext(r.Context(),
		`UPDATE content_moderation_queue SET manual_status=$1, reviewer=$2, review_note=$3, reviewed_at=now()
		 WHERE id=$4 RETURNING `+modQueueCols, ms, body.Reviewer, body.Note, id)
	q, err := scanModQueue(row)
	if err != nil {
		writeErr(w, 404, "queue item not found: "+err.Error())
		return
	}
	audDec := "manual_allow"
	if ms == manualBlock {
		audDec = "manual_block"
	} else if ms == manualFalse {
		audDec = "false_positive"
	}
	d.DB.ExecContext(r.Context(),
		`INSERT INTO audit_events(occurred_at,trace_id,message_id,action,decision,detail)
		 VALUES(now(),'',$1,'CONTENT_REVIEW',$2,$3::jsonb)`, q.Ref, audDec,
		`{"queue_id":`+strconv.Itoa(id)+`,"reviewer":"`+body.Reviewer+`"}`)
	writeJSON(w, 200, map[string]any{"status": "ok", "item": q})
}

func (d *Deps) adminModRulesList(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.QueryContext(r.Context(), `SELECT id,code,rule_type,pattern,action,enabled FROM content_rules ORDER BY id`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []modRule{}
	for rows.Next() {
		var x modRule
		if err := rows.Scan(&x.ID, &x.Code, &x.RuleType, &x.Pattern, &x.Action, &x.Enabled); err == nil {
			out = append(out, x)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "rules": out})
}

func (d *Deps) adminModRuleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		RuleType int    `json:"rule_type"`
		Pattern  string `json:"pattern"`
		Action   int    `json:"action"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Code) == "" || body.RuleType < 1 || body.RuleType > 4 || body.Action < 1 || body.Action > 2 {
		writeErr(w, 400, "code required; rule_type 1-4; action 1-2")
		return
	}
	var rl modRule
	err := d.DB.QueryRowContext(r.Context(),
		`INSERT INTO content_rules(code,rule_type,pattern,action,enabled) VALUES($1,$2,$3,$4,1)
		 RETURNING id,code,rule_type,pattern,action,enabled`,
		body.Code, body.RuleType, body.Pattern, body.Action).
		Scan(&rl.ID, &rl.Code, &rl.RuleType, &rl.Pattern, &rl.Action, &rl.Enabled)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"status": "ok", "rule": rl})
}

func (d *Deps) adminModRuleToggle(w http.ResponseWriter, r *http.Request) {
	id := atoiDefault(r.PathValue("id"), 0)
	var body struct {
		Enabled int `json:"enabled"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if id <= 0 || body.Enabled < 0 || body.Enabled > 1 {
		writeErr(w, 400, "bad id or enabled")
		return
	}
	var rl modRule
	err := d.DB.QueryRowContext(r.Context(),
		`UPDATE content_rules SET enabled=$1, updated_at=now() WHERE id=$2
		 RETURNING id,code,rule_type,pattern,action,enabled`, body.Enabled, id).
		Scan(&rl.ID, &rl.Code, &rl.RuleType, &rl.Pattern, &rl.Action, &rl.Enabled)
	if err != nil {
		writeErr(w, 404, "rule not found: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "rule": rl})
}
