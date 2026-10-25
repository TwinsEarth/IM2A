package msgseq

import (
	"sort"
	"sync"
	"testing"
)

// TestNextMonotonic 单用户序号严格递增、连续无空洞。
func TestNextMonotonic(t *testing.T) {
	g := New(1024)
	const n = 1000
	got := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		s, err := g.Next(7)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, s)
	}
	for i := 1; i < n; i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("seq not contiguous at %d: %d -> %d", i, got[i-1], got[i])
		}
	}
}

// TestNextBatch 批量序号连续。
func TestNextBatch(t *testing.T) {
	g := New(1024)
	seqs, err := g.NextBatch(7, 5)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(seqs) != 5 {
		t.Fatalf("got %d seqs, want 5", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("batch seq gap: %d -> %d", seqs[i-1], seqs[i])
		}
	}
	if _, err := g.NextBatch(7, 0); err != ErrInvalidBatch {
		t.Fatalf("batch 0 err=%v, want ErrInvalidBatch", err)
	}
}

// TestConcurrentNoDuplicates 100 用户 × 100 并发消息：无重复无空洞。
func TestConcurrentNoDuplicates(t *testing.T) {
	const users = 100
	const per = 100
	g := New(1024)
	var wg sync.WaitGroup
	results := make([][]uint64, users)
	for u := 0; u < users; u++ {
		u := u
		results[u] = make([]uint64, 0, per)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := 0; m < per; m++ {
				s, err := g.Next(uint64(u + 1))
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				results[u] = append(results[u], s)
			}
		}()
	}
	wg.Wait()
	for u := 0; u < users; u++ {
		seqs := append([]uint64(nil), results[u]...)
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		if len(seqs) != per {
			t.Fatalf("user %d got %d, want %d", u+1, len(seqs), per)
		}
		seen := map[uint64]bool{}
		for i, s := range seqs {
			if seen[s] {
				t.Fatalf("user %d duplicate seq %d", u+1, s)
			}
			seen[s] = true
			if i > 0 && s != seqs[i-1]+1 {
				t.Fatalf("user %d gap %d -> %d", u+1, seqs[i-1], s)
			}
		}
	}
}

// TestDifferentUsersIndependent 不同用户落在各自分片（可并行）。
func TestDifferentUsersIndependent(t *testing.T) {
	g := New(1024)
	a, _ := g.Next(1)
	b, _ := g.Next(2)
	if a != 1 || b != 1 {
		// 不同用户分片独立，首个序号都应从 1 开始；若恰好同分片则仍都为 1（各自分片计数）。
		t.Fatalf("first seq for independent users: a=%d b=%d, want 1/1", a, b)
	}
}
