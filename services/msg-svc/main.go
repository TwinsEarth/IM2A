// msg-svc：消息序号生成服务（§2.3）。
// 核心：64 分片锁 + FNV 哈希，避免全局互斥锁瓶颈。
// 运行：go run ./services/msg-svc -selftest   # 自测后退出
//
//	go run ./services/msg-svc -port 8081  # 启动 healthz
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/api"
	"human-agent-im/internal/config"
	"human-agent-im/internal/msgseq"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("msg-svc")

	if *selftest {
		if err := selfTest(); err != nil {
			log.Fatalf("[msg-svc] SELF-TEST FAILED: %v", err)
		}
		return
	}

	// v2.3.27：启动播种（详见 internal/msgseq 单例注释）。
	// 独立跑 msg-svc 时它自己就是发号进程，必须从 messages 表续上，否则重启归零撞号。
	// 注意：若与 im2a-gateway 同时运行，两个进程**各自**持有内存计数器，同一 sender
	// 仍会撞号——迁移 037 的唯一索引会把它变成显式错误，但正确做法是只跑其中一个。
	if dsn := os.Getenv("PG_DSN"); dsn != "" {
		if db, err := sql.Open("pgx", dsn); err != nil {
			log.Printf("[msg-svc] WARN open PG for seeding: %v", err)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			n, serr := msgseq.Default().SeedFromDB(ctx, db)
			cancel()
			if serr != nil {
				log.Printf("[msg-svc] WARN seq seed failed (将从 1 开始，可能与历史 seq 撞号): %v", serr)
			} else {
				log.Printf("[msg-svc] seq seeded from PG: %d senders", n)
			}
			_ = db.Close()
		}
		log.Println("[msg-svc] WARN 与 im2a-gateway 同时运行不安全：两进程内存计数器互不可见。" +
			"生产请只跑网关（/v1/msg/* 已由网关提供），或等 v3 kernel 的 DB 发号。")
	} else {
		log.Println("[msg-svc] WARN 未设置 PG_DSN：序号纯内存、重启归零；禁止与 im2a-gateway 同时对外提供 /v1/msg/*。")
	}

	mux := api.NewMsgMux(cfg.ServiceName)
	log.Printf("[msg-svc] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), mux))
}

// selfTest 并发验证：100 用户 × 100 消息，断言总量与单用户序号严格递增且无重复。
func selfTest() error {
	const users = 100
	const msgsPerUser = 100

	// 自测也用单例，保证"自测跑的分配器"与"线上跑的"是同一个实现。
	g := msgseq.Default()

	var wg sync.WaitGroup
	perUser := make([][]uint64, users)
	for u := 0; u < users; u++ {
		u := u
		perUser[u] = make([]uint64, 0, msgsPerUser)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := 0; m < msgsPerUser; m++ {
				seq, err := g.Next(uint64(u + 1))
				if err != nil {
					log.Printf("[msg-svc] Next error: %v", err)
					return
				}
				perUser[u] = append(perUser[u], seq)
			}
		}()
	}
	wg.Wait()

	// 断言：总量正确
	total := 0
	for u := 0; u < users; u++ {
		total += len(perUser[u])
	}
	if total != users*msgsPerUser {
		return fmt.Errorf("total seq = %d, want %d", total, users*msgsPerUser)
	}
	// 断言：单用户序号连续且唯一
	for u := 0; u < users; u++ {
		seqs := perUser[u]
		sorted := append([]uint64(nil), seqs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		seen := map[uint64]bool{}
		for i, s := range sorted {
			if seen[s] {
				return fmt.Errorf("user %d duplicate seq %d", u+1, s)
			}
			seen[s] = true
			if i > 0 && s != sorted[i-1]+1 {
				return fmt.Errorf("user %d seq gap: %d -> %d", u+1, sorted[i-1], s)
			}
		}
	}

	fmt.Printf("[msg-svc] SELF-TEST PASS: %d users × %d msgs, total=%d, %s\n",
		users, msgsPerUser, total, g.Summary())
	return nil
}
