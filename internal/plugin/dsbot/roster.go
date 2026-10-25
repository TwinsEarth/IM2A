// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/src/host/roster.js、src/host/permissions.js、
// src/host/prompt.js、src/host/constants.js (Apache-2.0)，
// 已修改为 Go 实现：只把 Bot 名册 / 角色 / 权限 / 群聊语义整理为只读 JSON 规格，
// 不复制其执行代码、不执行任何 Bot、不触碰 DSH 注入点。
package dsbotplugin

import "encoding/json"

// RosterSpecification 是 DS Bot 团队名册/角色/权限的只读规格（`RosterSpec()` 的返回类型）。
// 它是数据，不是实现：插件不执行任何 Bot，也不与 DSH 交互。
type RosterSpecification struct {
	Upstream        string         `json:"upstream"`
	UpstreamVersion string         `json:"upstreamVersion"`
	ReadOnly        bool           `json:"readOnly"`
	Sources         []string       `json:"sources"` // 规格取自哪些上游文件
	MainBot         MainBotSpec    `json:"mainBot"`
	Sessions        SessionSpec    `json:"sessions"`
	Bot             BotSpec        `json:"bot"`
	Roles           []RoleSpec     `json:"roles"`
	Permissions     PermissionSpec `json:"permissions"`
	Groups          GroupSpec      `json:"groups"`
	Tools           ToolsSpec      `json:"tools"`
	Limits          LimitSpec      `json:"limits"`
}

// MainBotSpec 是 Main Bot（Chief of Staff）的最高权限语义。
type MainBotSpec struct {
	RoleLabel    string   `json:"roleLabel"`
	Multiple     bool     `json:"multiple"`  // 团队可以有多个 Main Bot，权力相同
	Selection    string   `json:"selection"` // 上游在 state.mainBotIds 里记录 Main Bot 集合
	Powers       []string `json:"powers"`
	Restrictions []string `json:"restrictions"`
}

// SessionSpec 描述"每个 Bot 的会话"语义。
type SessionSpec struct {
	PerBot []SessionKind `json:"perBot"`
	Notes  []string      `json:"notes"`
}

// SessionKind 是 Bot 拥有的一类会话。
type SessionKind struct {
	ID          string `json:"id"`
	Purpose     string `json:"purpose"`
	SharedScope string `json:"sharedScope,omitempty"` // 该会话与其他成员共享的范围（群会话）
}

// BotSpec 是 Bot 记录本身的字段语义。
type BotSpec struct {
	NameMax      int         `json:"nameMax"`
	RoleLabelMax int         `json:"roleLabelMax"`
	Fields       []FieldSpec `json:"fields"`
	Note         string      `json:"note"`
}

// FieldSpec 是一个可配置的 Bot 字段。
type FieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// RoleSpec 是团队中的一个角色及其权力。
type RoleSpec struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Powers      []string `json:"powers"`
}

// PermissionSpec 是 DSH 权限预设在 DS Bot 里的继承/覆盖语义。
type PermissionSpec struct {
	Model             string   `json:"model"`
	PerBotPreset      bool     `json:"perBotPreset"`
	GroupOverride     bool     `json:"groupOverride"`
	SessionOnlyPreset string   `json:"sessionOnlyPreset"` // DSH 的 "auto" 只能用于单会话，不能作为 Bot/群权限
	Inheritance       []string `json:"inheritance"`
	UserOnly          []string `json:"userOnly"`
	Note              string   `json:"note"`
}

// GroupSpec 是群聊语义与上限。
type GroupSpec struct {
	MaxMembers  int      `json:"maxMembers"`
	NameMax     int      `json:"nameMax"`
	NoticeMax   int      `json:"noticeMax"`
	ReplyModes  []string `json:"replyModes"` // everyone | admin
	AdminRules  []string `json:"adminRules"`
	Fields      []string `json:"fields"`
	MemberRules []string `json:"memberRules"`
}

// ToolsSpec 是团队工具清单与 Main Bot 专属工具。
type ToolsSpec struct {
	All      []string `json:"all"`
	MainOnly []string `json:"mainOnly"`
	Note     string   `json:"note"`
}

// LimitSpec 是上游 constants.js 里与名册/消息相关的上限（与 chunk.go 常量同源）。
type LimitSpec struct {
	MessageInlineMax  int `json:"messageInlineMax"`
	LongHead          int `json:"longHead"`
	TextKept          int `json:"textKept"`
	GroupLineRead     int `json:"groupLineRead"`
	TextShowMax       int `json:"textShowMax"`
	ReadLineMax       int `json:"readLineMax"`
	OwnChatLineMax    int `json:"ownChatLineMax"`
	AnchorLineMax     int `json:"anchorLineMax"`
	RoomReplyTimeoutM int `json:"roomReplyTimeoutMs"`
	RoomStepGapMs     int `json:"roomStepGapMs"`
	RoomWorkGraceMs   int `json:"roomWorkGraceMs"`
	MaxExchanges      int `json:"maxExchanges"`
}

// RosterSpec 返回 DS Bot 团队名册 / 角色 / 权限的只读结构化规格。
// 说明：这是**语义对照数据**，供 IM2A 的 agentregistry 等模块参考；本插件不执行 Bot。
func RosterSpec() RosterSpecification {
	return RosterSpecification{
		Upstream:        "https://github.com/Fei2411/DeepSeek-Bot",
		UpstreamVersion: UpstreamDSBotVersion,
		ReadOnly:        true,
		Sources: []string{
			"ds-bot/src/host/roster.js",
			"ds-bot/src/host/permissions.js",
			"ds-bot/src/host/prompt.js",
			"ds-bot/src/host/constants.js",
			"AGENTS.md",
		},
		MainBot: MainBotSpec{
			RoleLabel: "Main Bot (Chief of Staff)",
			Multiple:  true, // 上游：The team can have more than one Main Bot; they share these powers.
			Selection: "state.mainBotIds[] (ordered; mainBotIds[0] is the first Main Bot)",
			Powers: []string{
				"create_bot：创建新 Bot（其它 Bot 无此权限）",
				"update_bot：修改其他 Bot 的名称/角色/颜色/头像/模型/思考级别/工作目录/指令",
				"stop_bot：结束其他 Bot 正在进行的回合（其它 Bot 只能停自己）",
				"create_group：创建群聊并默认成为其 admin",
				"update_group：修改任意群聊的名称/公告/成员/admin/回复模式/工作目录",
				"delete_group：归档群聊（仅 Main Bot 或用户）",
				"设置群聊工作目录（仅 Main Bot 或用户）",
				"读写其他 Bot 的记忆（memory）与能力（capabilities）",
				"创建/修改/删除团队级 capability 条目（被他人使用时）",
			},
			Restrictions: []string{
				"Main Bot 不能被删除；必须先移除其 Main Bot 角色",
				"权限预设（permission）与群聊 relay 上限只有用户能设置，Main Bot 不能",
				"群 admin 可以修改自己的群，但不能删除群、不能设置群工作目录",
				"任何 Bot 都可以修改自己的设置，但修改别的 Bot 需要 Main Bot 角色",
			},
		},
		Sessions: SessionSpec{
			PerBot: []SessionKind{
				{ID: "ownChat", Purpose: "Bot 与用户的私聊会话（长生命周期，独立记忆与电脑环境）"},
				{ID: "groupSession", Purpose: "Bot 在每个群聊里的独立会话；群成员看到它在群里的发言", SharedScope: "group"},
				{ID: "freshPart", Purpose: "上下文将满时切换到新的会话 part（带 handoff 说明 + 最近消息），旧 part 仍可读"},
			},
			Notes: []string{
				"每个 Bot 是长生命周期身份，其工作在 DSH Session 里跑；一个 Bot 自己的私聊 1 个会话、每个群 1 个会话",
				"上下文将满时开新 part；此前 part 保持可读，可用 read_own_chat 按锚点/搜索回读",
				"群会话与私聊会话彼此独立，群里的工作留在群里",
				"团队里另有一个普通 DSH Agent（非 Bot），其 Session 是普通根会话，不下发团队工具",
			},
		},
		Bot: BotSpec{
			NameMax:      40,
			RoleLabelMax: 24,
			Fields: []FieldSpec{
				{Name: "name", Type: "string", Description: "Bot 名称（团队内唯一，≤40 字）"},
				{Name: "role", Type: "string", Description: "两到四个字的角色标签（≤24 字）"},
				{Name: "instructions", Type: "string", Description: "该 Bot 的系统指令/身份说明"},
				{Name: "color", Type: "string", Description: "头像色板 id（从上游 COLORS/EXTRA_COLORS 选择，可缺省）"},
				{Name: "avatar", Type: "object", Description: "头像（shape 或受限大小的 data URL 图片）"},
				{Name: "model", Type: "string", Description: "创建时固定的模型引用；后续默认模型变化不影响它"},
				{Name: "effort", Type: "string", Description: "思考级别（依赖已选模型）"},
				{Name: "workDir", Type: "string", Description: "工作目录（绝对路径；不存在时回到团队工作区）"},
				{Name: "permission", Type: "string", Description: "DSH 权限预设名（Bot 级，应用到它的每个会话）"},
				{Name: "capabilities", Type: "array", Description: "挂载给该 Bot 的团队能力条目"},
				{Name: "createdBy", Type: "string", Description: "创建者 Bot id（用户创建时为空）"},
				{Name: "kickoff", Type: "object", Description: "可选的首条问候（含 Main Bot 标记）"},
			},
			Note: "名册存于上游 state.json（version 1：mainBotIds/bots/rooms/... 整体重写）；本插件只描述语义，不读写该状态。",
		},
		Roles: []RoleSpec{
			{
				ID: "user", Label: "User", Description: "团队所有者：最高裁决者",
				Powers: []string{
					"创建/删除任意 Bot 与群聊",
					"设置 Bot 的权限预设、群聊的权限预设与 relay 上限（仅用户）",
					"修改任意 Bot 的设置与任意群聊",
				},
			},
			{
				ID: "mainBot", Label: "Main Bot (Chief of Staff)", Description: "团队首席，可有多名，权力相同",
				Powers: []string{"创建/修改/停用/删除其他 Bot（删除前须先移除 Main 角色）", "创建/修改/归档群聊", "跨 Bot 读写记忆与能力", "把任务分派给合适的 Bot 或群聊"},
			},
			{
				ID: "bot", Label: "Bot", Description: "团队里的长生命周期成员，按自己的角色与指令工作",
				Powers: []string{"修改自己的设置（名称/角色/颜色/模型/思考级别/工作目录/指令）", "创建自己的群聊会话内的协作（@Name/message_bot 组内）", "读写自己的记忆与能力"},
			},
			{
				ID: "groupAdmin", Label: "Group admin", Description: "群聊的引导者，必须是群成员",
				Powers: []string{"修改自己管理的群聊（名称/公告/成员/admin/回复模式）", "在群里用 @Name 调度成员", "在管理群里发起的 post_to_group 会开新一轮讨论"},
			},
			{
				ID: "member", Label: "Group member", Description: "群聊成员",
				Powers: []string{"在群里发言、用 @Name 把工作交给其他成员", "post_to_group 只有被点名的成员会被调用"},
			},
		},
		Permissions: PermissionSpec{
			Model:             "DSH permission preset（如 read-only / workspace-write / danger-full-access，由 DSH 提供）",
			PerBotPreset:      true,
			GroupOverride:     true,
			SessionOnlyPreset: "auto",
			Inheritance: []string{
				"Bot 的 permission 应用到它的每个 Session（新 part、新群会话都继承）",
				"群聊可以给所有成员设一个统一预设，覆盖成员自己的预设；未设时成员用自己的",
				"群预设被移除后，成员回落到自己的 Bot 预设，再回落到 DSH 默认预设",
				"DSH 的 'auto'（Auto review）只对当前 Session 生效，不能作为 Bot 或群的权限预设",
			},
			UserOnly: []string{"Bot 的权限预设（setPermission）", "群聊的权限预设与 relay 上限"},
			Note:     "权限预设只在 Bot/群层保存；每次新会话按当时的 Bot 预设或群覆盖设置。设置失败只告警，不阻断其他会话。",
		},
		Groups: GroupSpec{
			MaxMembers: 12,
			NameMax:    60,
			NoticeMax:  2000,
			ReplyModes: []string{"everyone", "admin"},
			AdminRules: []string{
				"群可以有 0 或 1 个 admin；admin 必须是成员",
				"用户创建的群，admin 默认是该 Main Bot 创建者或任一 Main Bot 成员",
				"Main Bot 领导自己创建的群；admin 不能把群删掉，也不能设置群工作目录",
				"admin 移交/离开时必须先把 admin 交给另一名成员",
			},
			Fields: []string{"name", "notice", "members", "admin", "mode", "permission", "relayLimit", "workDir"},
			MemberRules: []string{
				"「everyone」模式：每条普通消息每个成员依次回复；「admin」模式：先由 admin 决定是否 @ 其他成员（新群默认 admin）",
				"群成员最多 12 个 Bot；群里的工作留在群里（message_bot 只到群成员）",
				"群里的每个成员有各自的群会话，群记录是共享账本",
			},
		},
		Tools: ToolsSpec{
			All: []string{
				"list_bots", "message_bot", "stop_bot", "create_bot", "update_bot", "list_models",
				"create_group", "update_group", "delete_group", "post_to_group", "ask_user", "show_view",
				"request_secret", "list_secrets", "group_relay", "read_own_chat",
				"read_group_chat", "remember", "forget", "recall", "read_browser",
				"list_capabilities", "save_capability", "delete_capability",
				"attach_capability", "detach_capability",
			},
			MainOnly: []string{
				"create_bot", "update_bot（改其他 Bot）", "stop_bot（停其他 Bot）", "create_group",
				"delete_group", "update_group（改群工作目录/删群时）",
				"读写其他 Bot 的 memory / capabilities",
			},
			Note: "工具清单取自上游 constants.js 的 TEAM_TOOLS；本插件只输出清单，不注册也不执行这些工具。",
		},
		Limits: LimitSpec{
			MessageInlineMax:  MessageInlineMax,
			LongHead:          LongHead,
			TextKept:          TextKept,
			GroupLineRead:     GroupLineRead,
			TextShowMax:       TextShowMax,
			ReadLineMax:       2000,
			OwnChatLineMax:    400,
			AnchorLineMax:     8000,
			RoomReplyTimeoutM: 240000,
			RoomStepGapMs:     30000,
			RoomWorkGraceMs:   15000,
			MaxExchanges:      600,
		},
	}
}

// RosterSpecJSON 返回 RosterSpec() 的缩进 JSON（供只读端点直接输出）。
func RosterSpecJSON() ([]byte, error) {
	return json.MarshalIndent(RosterSpec(), "", "  ")
}
