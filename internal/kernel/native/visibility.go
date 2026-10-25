// SPDX-License-Identifier: MIT
package native

import (
	"context"
	"database/sql"
	"strconv"
)

// 信任档位（对齐 trust_relations.trust_level 与 L0–L4 矩阵）。
//
//	0=self 1=family 2=trusted 3=known 4=stranger（无关系/查询失败一律按 L4 fail-closed）
const (
	TrustSelf     = 0
	TrustFamily   = 1
	TrustTrusted  = 2
	TrustKnown    = 3
	TrustStranger = 4
)

// VisibilityLevel 返回 observer 对 subject 的信任档：
//   - observer==subject → TrustSelf；
//   - 无 trust_relations 行、DB 不可用、异常值 → TrustStranger（L4，**fail-closed**，不泄露存在性）；
//   - 其它按表值截断到 0..3。
//
// 该函数只读取 `trust_relations`（036_missing_tables.sql:100），不做任何写操作。
func VisibilityLevel(ctx context.Context, db *sql.DB, observer, subject int64) int {
	if observer <= 0 || subject <= 0 {
		return TrustStranger
	}
	if observer == subject {
		return TrustSelf
	}
	if db == nil {
		return TrustStranger
	}
	var level int
	err := db.QueryRowContext(ctx,
		`SELECT trust_level FROM trust_relations WHERE owner_id = $1 AND contact_id = $2`,
		strconv.FormatInt(observer, 10), strconv.FormatInt(subject, 10)).Scan(&level)
	if err != nil { // sql.ErrNoRows / DB 错误：L4 fail-closed
		return TrustStranger
	}
	if level < 0 || level > TrustKnown {
		return TrustStranger
	}
	return level
}
