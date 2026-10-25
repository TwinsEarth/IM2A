package msgseq

// v2.3.27（task-12）：单例与播种的单元测试。
//
// 背景：进程内曾有 3 处独立 msqseq.New(1024)（api/msg.go 两处、msg-svc 一处）。
// 注意 1024 是 splitThresh（拆片信号阈值），不是分片数——分片数固定 64
// （defaultShards），所以"播种要按分片算术"其实是"按 sender 落到的那个分片写 map"，
// 由 shardIndex 保证，Seed 与 Next 走的是同一函数。

import (
	"context"
	"sync"
	"testing"
)

func TestDefaultSingletonShared(t *testing.T) {
	prev := SetDefault(New(1024))
	t.Cleanup(func() { SetDefault(prev) })

	const uid = 424242
	a, err := Default().Next(uid)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// 再取一次 Default() 必须是同一个实例，否则序号会重复。
	b, err := Default().Next(uid)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if b != a+1 {
		t.Fatalf("Default() 不是单例：%d -> %d（应连续）", a, b)
	}
}

func TestSeedSetsStartAndNeverGoesBack(t *testing.T) {
	g := New(1024)
	g.Seed(7, 41)
	if s, _ := g.Next(7); s != 42 {
		t.Fatalf("Seed(41) 后首个序号 = %d, want 42", s)
	}
	// 播种只升不降：重复用更小的值播种不能把游标退回去（否则会重发已用过的 seq）。
	g.Seed(7, 5)
	if s, _ := g.Next(7); s != 43 {
		t.Fatalf("低值播种把游标退回了：%d, want 43", s)
	}
	// 更大的播种值生效（模拟另一进程已发到更远的号）。
	g.Seed(7, 100)
	if s, _ := g.Next(7); s != 101 {
		t.Fatalf("高值播种未生效：%d, want 101", s)
	}
	// 不同 sender 互不影响（分片内是 per-user map）。
	if s, _ := g.Next(8); s != 1 {
		t.Fatalf("其他 sender 不应被播种影响：%d, want 1", s)
	}
}

// TestSeedPerSenderNotGlobalMax 固定一个曾经被讨论过的错误实现：
// 用"全局 MAX(seq)"播种会让所有 sender 一起跳到最大发送者的号，产生巨大空洞。
// 这里直接验证 Seed 的语义是按 sender 的，而 SeedFromDB 的 SQL 是 GROUP BY sender_id
// （见 msgseq.go），两者合起来保证不会发生全局跳号。
func TestSeedPerSenderNotGlobalMax(t *testing.T) {
	g := New(1024)
	g.Seed(1, 5)   // sender 1 已用到 5
	g.Seed(2, 999) // sender 2 已用到 999
	if s, _ := g.Next(1); s != 6 {
		t.Fatalf("sender1 首个序号 = %d, want 6（不应被 sender2 的 999 带跑）", s)
	}
	if s, _ := g.Next(2); s != 1000 {
		t.Fatalf("sender2 首个序号 = %d, want 1000", s)
	}
}

func TestSeedFromDBNilIsNoop(t *testing.T) {
	g := New(1024)
	n, err := g.SeedFromDB(context.Background(), nil)
	if err != nil || n != 0 {
		t.Fatalf("nil db: n=%d err=%v, want 0/nil", n, err)
	}
	if s, _ := g.Next(9); s != 1 {
		t.Fatalf("nil db 后序号应仍从 1 开始, got %d", s)
	}
}

// TestSingletonConcurrent1000 单例下并发取号无重复（任务要求的 ①）。
func TestSingletonConcurrent1000(t *testing.T) {
	prev := SetDefault(New(1024))
	t.Cleanup(func() { SetDefault(prev) })

	const workers = 10
	const perWorker = 100
	g := Default()
	const uid = 777001

	var mu sync.Mutex
	seen := make(map[uint64]bool, workers*perWorker)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				s, err := g.Next(uid)
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				mu.Lock()
				if seen[s] {
					t.Errorf("重复序号 %d", s)
				}
				seen[s] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*perWorker {
		t.Fatalf("取到 %d 个不同序号, want %d", len(seen), workers*perWorker)
	}
}
