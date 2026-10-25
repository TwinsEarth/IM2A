package api

// v2.3.27（task-12）：seq 持久化的 PG 判定性测试。PG_DSN 未设置时自动 skip。
//
// 覆盖两个断言：
//   1) 迁移 037 建的 uq_messages_sender_seq 真的能挡住重复 (sender_id, seq)；
//   2) 启动播种（msgseq.SeedFromDB）让 Next 从该 sender 的 MAX(seq)+1 继续，
//      而不是从 1 重新开始（重启归零的直接回归）。
//
// 运行：
//   PG_DSN='postgres://...' go test ./internal/api -run TestSeqPersistence -v

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"human-agent-im/internal/msgseq"
	"human-agent-im/internal/store"
)

func realDepsForSeq(t *testing.T) *Deps {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN 未设置，跳过真实 PG 测试")
	}
	d, err := NewDeps("pgx", dsn)
	if err != nil || d == nil || d.DB == nil {
		t.Skipf("连接 PG 失败（跳过而不是失败）: %v", err)
	}
	// 池上限 25，PG max_connections=30：不关池会打满同包后续测试（53300）。
	t.Cleanup(func() { _ = d.DB.Close() })
	return d
}

// TestSeqPersistence_UniqueIndexBlocksDuplicate 证明重复 (sender_id,seq) 现在会显式失败。
func TestSeqPersistence_UniqueIndexBlocksDuplicate(t *testing.T) {
	d := realDepsForSeq(t)
	ctx := context.Background()

	// 迁移可能因历史重复数据而**跳过**建索引（037 的守卫）。这种情况下本测试无法断言，
	// 明确 skip 并打印数量，避免把"环境有脏数据"误报成"代码坏了"。
	var dupGroups int64
	if err := d.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT 1 FROM messages GROUP BY sender_id, seq HAVING count(*) > 1) x`).
		Scan(&dupGroups); err != nil {
		t.Fatalf("count dup: %v", err)
	}
	var idxExists bool
	if err := d.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname='uq_messages_sender_seq')`).
		Scan(&idxExists); err != nil {
		t.Fatalf("check index: %v", err)
	}
	if !idxExists {
		t.Skipf("uq_messages_sender_seq 不存在（库里有 %d 组重复，迁移按设计跳过）。请按 037 注释清理后重跑本测试。", dupGroups)
	}

	sender := time.Now().UnixNano()%1_000_000 + 9_000_000 // 独立 sender，避免撞既有数据
	const seq = 424242
	cleanup := func() {
		_, _ = d.DB.ExecContext(ctx, `DELETE FROM messages WHERE sender_id=$1 AND seq=$2`, sender, seq)
	}
	cleanup()
	t.Cleanup(cleanup)

	ins := `INSERT INTO messages(message_id, sender_id, recipient_id, biz_type, content, seq)
		VALUES ($1,$2,$3,'text','dup-test',$4)`
	if _, err := d.DB.ExecContext(ctx, ins, "seq-dup-a-"+time.Now().Format("150405.000000"), sender, sender+1, seq); err != nil {
		t.Fatalf("第一条插入失败: %v", err)
	}
	_, err := d.DB.ExecContext(ctx, ins, "seq-dup-b-"+time.Now().Format("150405.000000"), sender, sender+1, seq)
	if err == nil {
		t.Fatalf("第二条相同 (sender_id=%d, seq=%d) 竟然插入成功：唯一索引没生效", sender, seq)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("期望 23505 unique_violation，实际: %v", err)
	}
}

// TestSeqPersistence_SeedFromDBContinuesAfterMax 证明播种后不会从 1 重来。
func TestSeqPersistence_SeedFromDBContinuesAfterMax(t *testing.T) {
	d := realDepsForSeq(t)
	ctx := context.Background()

	sender := time.Now().UnixNano()%1_000_000 + 8_000_000
	cleanup := func() {
		_, _ = d.DB.ExecContext(ctx, `DELETE FROM messages WHERE sender_id=$1`, sender)
	}
	cleanup()
	t.Cleanup(cleanup)

	// 该 sender 已发到 seq=7（故意留空洞 5,7 —— 播种取 MAX，不要求连续）。
	for i, seq := range []int64{5, 7} {
		if _, err := d.DB.ExecContext(ctx,
			`INSERT INTO messages(message_id, sender_id, recipient_id, biz_type, content, seq)
			 VALUES ($1,$2,$3,'text','seed-test',$4)`,
			"seq-seed-"+time.Now().Format("150405.000000")+"-"+string(rune('a'+i)), sender, sender+1, seq); err != nil {
			t.Fatalf("插入历史 seq 失败: %v", err)
		}
	}

	// 用一个干净分配器播种（并还原全局单例，避免污染其它测试）。
	prev := msgseq.SetDefault(msgseq.New(1024))
	t.Cleanup(func() { msgseq.SetDefault(prev) })

	n, err := msgseq.Default().SeedFromDB(ctx, d.DB)
	if err != nil {
		t.Fatalf("SeedFromDB: %v", err)
	}
	if n < 1 {
		t.Fatalf("应至少播种 1 个 sender, got %d", n)
	}
	next, err := msgseq.Default().Next(uint64(sender))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next != 8 {
		t.Fatalf("播种后首个序号 = %d, want 8（MAX(seq)=7 + 1）——播种没生效，重启仍会撞号", next)
	}
}

// 确保 store 包被引用（NewDeps 已间接使用；此处显式引用便于以后扩展断言）。
var _ = store.NewMemoryStore
