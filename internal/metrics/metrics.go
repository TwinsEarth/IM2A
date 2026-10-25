// Package metrics 提供 v1.9.9 Prometheus 文本格式指标：纯 stdlib，并发安全。
// 不引入外部依赖，输出符合 Prometheus text exposition format。
package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

var (
	mu        sync.Mutex
	counters  = map[string]map[string]int64{} // name -> labelSet -> value
	summaries = map[string][]float64{}        // name -> 延迟样本（ms）
)

// IncCounter 对带标签的计数器 +1。labelSet 如 "path=/v1/msg/send,code=201"。
func IncCounter(name, labelSet string) {
	mu.Lock()
	defer mu.Unlock()
	if counters[name] == nil {
		counters[name] = map[string]int64{}
	}
	counters[name][labelSet]++
}

// ObserveDuration 记录一次请求耗时（毫秒）。
func ObserveDuration(name string, ms float64) {
	mu.Lock()
	defer mu.Unlock()
	summaries[name] = append(summaries[name], ms)
}

// Render 输出 Prometheus 文本格式。
func Render() string {
	mu.Lock()
	defer mu.Unlock()
	out := ""
	names := make([]string, 0, len(counters))
	for n := range counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out += fmt.Sprintf("# HELP %s 计数\n# TYPE %s counter\n", n, n)
		labels := make([]string, 0, len(counters[n]))
		for ls := range counters[n] {
			labels = append(labels, ls)
		}
		sort.Strings(labels)
		for _, ls := range labels {
			out += fmt.Sprintf("%s{%s} %d\n", n, ls, counters[n][ls])
		}
	}
	// 延迟汇总：count/avg/p99
	for n, samples := range summaries {
		if len(samples) == 0 {
			continue
		}
		sorted := make([]float64, len(samples))
		copy(sorted, samples)
		sort.Float64s(sorted)
		sum := 0.0
		for _, v := range sorted {
			sum += v
		}
		p99 := sorted[int(float64(len(sorted)-1)*0.99)]
		out += fmt.Sprintf("# HELP %s 毫秒\n# TYPE %s summary\n", n, n)
		out += fmt.Sprintf("%s_count %d\n", n, len(sorted))
		out += fmt.Sprintf("%s_avg_ms %.2f\n", n, sum/float64(len(sorted)))
		out += fmt.Sprintf("%s_p99_ms %.2f\n", n, p99)
	}
	return out
}

// Handler 返回 /metrics 端点。
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, Render())
	})
}

// Middleware 包装 handler，真实记录每个请求的计数与延迟。
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(ww, r)
		ms := float64(time.Since(start).Microseconds()) / 1000.0
		ls := fmt.Sprintf(`path=%q,code=%d`, r.URL.Path, ww.code)
		IncCounter("http_requests_total", ls)
		ObserveDuration("http_request_duration_ms", ms)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// Flush 转发底层 Flusher，保证 SSE 流式接口不被中间件吞掉。
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传 http.Hijacker（2026-10-25 P1 修复，task-20 收尾）：
// main 是 `http.ListenAndServe(addr, metrics.Middleware(mux))`，/v1/ws 的 gorilla Upgrade
// 直接断言 http.Hijacker；本包装此前只有 WriteHeader/Flush，导致升级在 metrics 层必然失败。
// 底层不支持时返回 http.ErrNotSupported，与标准库语义一致。
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	// WebSocket 升级成功 = 101（否则 /metrics 把连接记为 200）。
	if s.code == 0 || s.code == http.StatusOK {
		s.code = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}
