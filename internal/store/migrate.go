package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
)

// migrations 内嵌全部幂等 schema（所有 DDL 均为 IF NOT EXISTS / OR REPLACE，
// 可在启动时安全重复执行）。内嵌进二进制，避免新环境"忘了手动 psql"导致缺表。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockKey 是 IM2A 迁移专用的 PostgreSQL advisory lock 常量（'IM2A' 四字节）。
// 跨进程/跨连接串行化迁移：多个服务实例同时启动、或 go test 跨包并行连库时，
// 同一时刻只有一个执行者执行 DDL，避免并发 CREATE TABLE/INDEX/触发器互等死锁。
const migrationLockKey int64 = 0x494D3241

// execer 抽象 *sql.DB 与 *sql.Conn 共有的 ExecContext。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Migrate 按文件名顺序执行内嵌迁移，幂等。传入的 db 须具备建表/建扩展权限。
// 通过 PostgreSQL 会话级 advisory lock 并绑定单连接，保证全集群同一时刻仅一个执行者做 DDL。
func Migrate(ctx context.Context, db *sql.DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("store: read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// 绑定单连接：会话级 advisory lock 与后续 DDL 必须落在同一物理连接，
	// 否则连接池可能把 lock 与 DDL 调度到不同连接导致锁失效。
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire migration conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		// 非 PG 后端不支持 advisory lock：回退到连接池直接执行（内存后端不经过 Migrate）。
		return applyMigrations(ctx, db, names)
	}
	// 连接关闭时 PG 会自动释放会话级锁，这里再显式 unlock 双保险。
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey)
	return applyMigrations(ctx, conn, names)
}

func applyMigrations(ctx context.Context, ex execer, names []string) error {
	for _, name := range names {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		if _, err := ex.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
	}
	return nil
}
