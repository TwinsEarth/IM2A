// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/src/host/roster.js 等文件的语义断言 (Apache-2.0)，
// 已修改为 Go 测试：验证只读规格覆盖 Main Bot/多 Main Bot/三种会话/角色/权限语义。

package dsbotplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRosterSpecCoreSemantics(t *testing.T) {
	spec := RosterSpec()
	if !spec.ReadOnly {
		t.Fatal("RosterSpec must be read-only data")
	}
	if spec.Upstream != "https://github.com/Fei2411/DeepSeek-Bot" || spec.UpstreamVersion != UpstreamDSBotVersion {
		t.Fatalf("upstream = %q %q", spec.Upstream, spec.UpstreamVersion)
	}
	if len(spec.Sources) < 3 {
		t.Fatalf("sources = %v, want the upstream files the spec came from", spec.Sources)
	}
}

func TestRosterSpecMainBot(t *testing.T) {
	spec := RosterSpec().MainBot
	if !spec.Multiple {
		t.Fatal("MainBot.Multiple must be true (上游允许多名 Main Bot 且权力相同)")
	}
	if spec.RoleLabel != "Main Bot (Chief of Staff)" {
		t.Fatalf("roleLabel = %q", spec.RoleLabel)
	}
	if len(spec.Powers) < 5 {
		t.Fatalf("main bot powers = %v, want the highest-rights list", spec.Powers)
	}
	joined := strings.Join(append(spec.Powers, spec.Restrictions...), "\n")
	for _, want := range []string{"create_bot", "update_bot", "delete_group", "不能被删除", "只有用户能设置"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("main bot semantics missing %q:\n%s", want, joined)
		}
	}
}

func TestRosterSpecSessions(t *testing.T) {
	spec := RosterSpec().Sessions
	if len(spec.PerBot) != 3 {
		t.Fatalf("sessions = %+v, want exactly three kinds per bot", spec.PerBot)
	}
	ids := map[string]bool{}
	for _, kind := range spec.PerBot {
		ids[kind.ID] = true
		if kind.Purpose == "" {
			t.Fatalf("session %q has no purpose", kind.ID)
		}
	}
	for _, want := range []string{"ownChat", "groupSession", "freshPart"} {
		if !ids[want] {
			t.Fatalf("session kind %q missing: %+v", want, spec.PerBot)
		}
	}
	if len(spec.Notes) == 0 {
		t.Fatal("sessions notes must explain long-lived identity + readable earlier parts")
	}
}

func TestRosterSpecRolesAndPermissions(t *testing.T) {
	spec := RosterSpec()
	roles := map[string]RoleSpec{}
	for _, role := range spec.Roles {
		roles[role.ID] = role
		if len(role.Powers) == 0 {
			t.Fatalf("role %q has no powers", role.ID)
		}
	}
	for _, want := range []string{"user", "mainBot", "bot", "groupAdmin", "member"} {
		if _, ok := roles[want]; !ok {
			t.Fatalf("role %q missing: %+v", want, spec.Roles)
		}
	}
	if !strings.Contains(strings.Join(roles["mainBot"].Powers, "\n"), "创建") {
		t.Fatalf("mainBot powers = %v", roles["mainBot"].Powers)
	}

	perms := spec.Permissions
	if !perms.PerBotPreset || !perms.GroupOverride {
		t.Fatalf("permissions = %+v, want bot preset + group override", perms)
	}
	if perms.SessionOnlyPreset != "auto" {
		t.Fatalf("sessionOnlyPreset = %q (DSH Auto review is session-only)", perms.SessionOnlyPreset)
	}
	inheritance := strings.Join(perms.Inheritance, "\n")
	for _, want := range []string{"每个 Session", "覆盖", "回落", "auto"} {
		if !strings.Contains(inheritance, want) {
			t.Fatalf("permission inheritance missing %q:\n%s", want, inheritance)
		}
	}
	if !strings.Contains(strings.Join(perms.UserOnly, "\n"), "群聊") {
		t.Fatalf("userOnly = %v, want group permission/relay limit to be user-only", perms.UserOnly)
	}
}

func TestRosterSpecGroupsAndTools(t *testing.T) {
	spec := RosterSpec()
	groups := spec.Groups
	if groups.MaxMembers != 12 || groups.NameMax != 60 || groups.NoticeMax != 2000 {
		t.Fatalf("group limits = %+v, want upstream ROOM_* constants", groups)
	}
	if strings.Join(groups.ReplyModes, ",") != "everyone,admin" {
		t.Fatalf("reply modes = %v", groups.ReplyModes)
	}
	if len(groups.AdminRules) == 0 || len(groups.MemberRules) == 0 {
		t.Fatalf("group rules missing: %+v", groups)
	}

	tools := spec.Tools
	all := strings.Join(tools.All, ",")
	for _, want := range []string{"create_bot", "message_bot", "post_to_group", "read_group_chat"} {
		if !strings.Contains(all, want) {
			t.Fatalf("tools.all missing %q", want)
		}
	}
	if !strings.Contains(strings.Join(tools.MainOnly, "\n"), "create_bot") {
		t.Fatalf("tools.mainOnly = %v, want create_bot", tools.MainOnly)
	}
	if !strings.Contains(tools.Note, "不注册") {
		t.Fatalf("tools note = %q, must disclose the spec is not an execution surface", tools.Note)
	}
}

func TestRosterSpecLimitsMatchUpstream(t *testing.T) {
	limits := RosterSpec().Limits
	checks := map[string]int{
		"messageInlineMax":  limits.MessageInlineMax,
		"longHead":          limits.LongHead,
		"textKept":          limits.TextKept,
		"groupLineRead":     limits.GroupLineRead,
		"textShowMax":       limits.TextShowMax,
		"readLineMax":       limits.ReadLineMax,
		"ownChatLineMax":    limits.OwnChatLineMax,
		"anchorLineMax":     limits.AnchorLineMax,
		"roomReplyTimeoutM": limits.RoomReplyTimeoutM,
		"roomStepGapMs":     limits.RoomStepGapMs,
		"roomWorkGraceMs":   limits.RoomWorkGraceMs,
		"maxExchanges":      limits.MaxExchanges,
	}
	want := map[string]int{
		"messageInlineMax": 20000, "longHead": 1000, "textKept": 2000, "groupLineRead": 20000,
		"textShowMax": 200000, "readLineMax": 2000, "ownChatLineMax": 400, "anchorLineMax": 8000,
		"roomReplyTimeoutM": 240000, "roomStepGapMs": 30000, "roomWorkGraceMs": 15000, "maxExchanges": 600,
	}
	for name, got := range checks {
		if got != want[name] {
			t.Fatalf("%s = %d, want %d (upstream constants.js)", name, got, want[name])
		}
	}
}

func TestRosterSpecJSON(t *testing.T) {
	raw, err := RosterSpecJSON()
	if err != nil {
		t.Fatalf("RosterSpecJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("RosterSpecJSON is not valid JSON: %v", err)
	}
	for _, key := range []string{"upstream", "readOnly", "mainBot", "sessions", "roles", "permissions", "groups", "tools", "limits"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("spec JSON missing key %q", key)
		}
	}
	if decoded["readOnly"] != true {
		t.Fatalf("readOnly = %v, want true", decoded["readOnly"])
	}
}
