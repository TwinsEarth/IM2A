// IM2A v1.9.7 全链路压测：真实打网关，统计 QPS / 延迟分位 / 错误率。
// 用法：go run ./scripts/loadtest -base http://127.0.0.1:8080 -token im2a_demo_token -c 1000 -n 10000
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "gateway base url")
	token := flag.String("token", "im2a_demo_token", "static bearer token")
	conc := flag.Int("c", 1000, "并发 goroutine")
	total := flag.Int("n", 10000, "总请求数")
	mode := flag.String("mode", "send", "seq | send")
	flag.Parse()

	transport := &http.Transport{MaxIdleConns: 2000, MaxIdleConnsPerHost: 2000}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}

	var latMu sync.Mutex
	var lat []time.Duration
	var ok, fail int64
	var codeMu sync.Mutex
	codes := map[int]int{}

	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < *total; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			t0 := time.Now()
			var code int
			if *mode == "send" {
				code = doSend(client, *base, *token, i)
			} else {
				code = doSeq(client, *base, *token, i)
			}
			d := time.Since(t0)
			latMu.Lock()
			lat = append(lat, d)
			latMu.Unlock()
			codeMu.Lock()
			codes[code]++
			codeMu.Unlock()
			if code >= 200 && code < 300 {
				atomic.AddInt64(&ok, 1)
			} else {
				atomic.AddInt64(&fail, 1)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
	p := func(q float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(float64(len(lat)-1)*q)]
	}
	fmt.Printf("=== loadtest mode=%s c=%d n=%d ===\n", *mode, *conc, *total)
	fmt.Printf("耗时=%s  QPS=%.1f  ok=%d  fail=%d\n", elapsed, float64(*total)/elapsed.Seconds(), ok, fail)
	fmt.Printf("延迟: avg=%s  p50=%s  p90=%s  p99=%s  p999=%s  max=%s\n",
		avg(lat), p(0.5), p(0.9), p(0.99), p(0.999), p(1.0))
	fmt.Printf("状态码分布: %v\n", codes)
}

func avg(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return sum / time.Duration(len(ds))
}

func doSeq(c *http.Client, base, tok string, i int) int {
	body, _ := json.Marshal(map[string]any{"userId": 99000000 + int64(i%1000)})
	req, _ := http.NewRequest("POST", base+"/v1/msg/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func doSend(c *http.Client, base, tok string, i int) int {
	body, _ := json.Marshal(map[string]any{
		"senderId":   99000000 + int64(i%1000),
		"receiverId": 99000100,
		"content":    fmt.Sprintf("loadtest msg %d", i),
		"bizType":     "loadtest",
	})
	req, _ := http.NewRequest("POST", base+"/v1/msg/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}
