package agentregistry

import (
	"context"
	"database/sql"
)

// v2.3.24：注册表接生产——首消息自动开通（master + 五个子 Agent）+ 门控读真实信任档。
//
// 与 Ensure 的区别：Provision 只在"首次"插入（ON CONFLICT DO NOTHING），
// 绝不覆盖运营/管理员后续调整的 trust_level / status / 配额；
// 随后返回该用户 master 的真实 trust_level，供 L4 PermissionGate 使用。
//
// agent_id 命名与 orchestration 保持一致：
//   master / schedule / procurement / legal / parenting / property。

// DefaultTrust 新自助绑定用户的默认信任档（3=熟人，可执行高风险动作但仍受预算门控）。
const DefaultTrust = 3

// DefaultQuota 新开通 Agent 的每周期调用上限。
const DefaultQuota = 500

// spec 是一个 Agent 的开通模板。
type spec struct {
	agentID     string
	role        string
	displayName string
}

var defaultAgents = []spec{
	{"master", "master", "管家"},
	{"schedule", "sub", "日程助理"},
	{"procurement", "sub", "采购助理"},
	{"legal", "sub", "法务助理"},
	{"parenting", "sub", "亲子助理"},
	{"property", "sub", "物业助理"},
}

// TrustLevel 满足 orchestration.TrustProvider：
// 幂等开通该用户的全部标准 Agent（不覆盖任何已存在记录），并返回 master 的真实信任档。
// master 缺失（异常）时返回 ErrNotFound。
func (s *Store) TrustLevel(ctx context.Context, userWxid string) (int, error) {
	if userWxid == "" {
		return 0, ErrNotFound
	}
	for _, a := range defaultAgents {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO agent_registry (user_wxid, agent_id, role, display_name, trust_level, quota_max)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (user_wxid, agent_id) DO NOTHING`,
			userWxid, a.agentID, a.role, a.displayName, DefaultTrust, DefaultQuota); err != nil {
			return 0, err
		}
	}
	var trust int
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT trust_level, status FROM agent_registry WHERE user_wxid=$1 AND agent_id='master'`,
		userWxid).Scan(&trust, &status)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != "active" {
		return 0, ErrPaused
	}
	return trust, nil
}
