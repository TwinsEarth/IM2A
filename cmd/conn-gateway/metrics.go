package main

// metrics.go：连接网关的 Prometheus 文本指标（stdlib-only，无依赖）。
//
// 指标名与告警阈值以 B2B-DESIGN.md §4.2 为准；这里是唯一实现点，不要在别处硬编码字符串。
//
// 指标清单：
//   conn_gateway_conns                            gauge    当前连接数
//   conn_gateway_uds_connected                    gauge    内核 UDS 连接数
//   conn_gateway_frames_in_total                  counter  内核经 UDS 推入并被接受的帧数
//   conn_gateway_frames_out_total                 counter  写到 WS 客户端的帧数
//   conn_gateway_frames_dropped_total{reason}     counter  reason=ring|shard|ackq|uds
//   conn_gateway_slow_kicks_total                 counter  慢消费者被断开
//   conn_gateway_degrade_total                    counter  进入 80% 降级
//   conn_gateway_ack_lag_seconds                  gauge    网关侧最早未确认帧的滞留时长（代理值）
//   kernel_ack_lag_seconds                        gauge    内核 StatsResp 上报的 ack lag（若有）
//   conn_gateway_uds_messages_total{kind}         counter  按 kind 统计的 UDS 收包

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

type metricKey struct {
	name   string
	labels string // 已格式化好的 `k="v"`；空表示无标签
}

type metricsRegistry struct {
	mu       sync.Mutex
	counters map[metricKey]float64
	gauges   map[string]float64
	help     map[string]string
	typ      map[string]string
	// unobserved 标记"尚未观测"的 gauge：渲染为 NaN（Prometheus 约定），
	// 而不是 0 —— 否则监控会把"没数据"误判成"零延迟/健康"。
	unobserved map[string]bool
}

func newMetricsRegistry() *metricsRegistry {
	m := &metricsRegistry{
		counters: map[metricKey]float64{},
		gauges:     map[string]float64{},
		help:       map[string]string{},
		typ:        map[string]string{},
		unobserved: map[string]bool{},
	}
	m.define("conn_gateway_conns", "gauge", "current native connections")
	m.define("conn_gateway_uds_connected", "gauge", "kernel UDS peers")
	m.define("conn_gateway_frames_in_total", "counter", "frames accepted from kernel via UDS")
	m.define("conn_gateway_frames_out_total", "counter", "frames written to WS clients")
	m.define("conn_gateway_frames_dropped_total", "counter", "frames dropped by gateway backpressure")
	m.define("conn_gateway_slow_kicks_total", "counter", "connections kicked as slow consumer")
	m.define("conn_gateway_degrade_total", "counter", "connections entered 80% degrade mode")
	m.define("conn_gateway_ack_lag_seconds", "gauge", "oldest unacked frame age observed by gateway")
	m.define("kernel_ack_lag_seconds", "gauge", "kernel-reported ack lag (from StatsResp)")
	m.define("conn_gateway_uds_messages_total", "counter", "UDS messages received by kind")
	m.define("conn_gateway_presence_state_fanout_total", "counter", "PRESENCE_STATE frames fanned out to local conns")
	m.define("conn_gateway_presence_state_dropped_total", "counter", "PRESENCE_STATE frames dropped (no local conn for userID)")

	// 预置零值：保证 /metrics 在"还没发生任何丢弃/踢人"时也能拿到完整指标名，
	// 否则运维会误以为指标缺失（B2B-DESIGN §4.2 要求这些指标必须可观测）。
	for _, name := range []string{
		"conn_gateway_frames_in_total", "conn_gateway_frames_out_total",
		"conn_gateway_slow_kicks_total", "conn_gateway_degrade_total",
		"conn_gateway_presence_state_fanout_total", "conn_gateway_presence_state_dropped_total",
	} {
		m.counters[metricKey{name: name}] = 0
	}
	for _, reason := range []string{"ring", "shard", "ackq", "uds", "lease"} {
		m.counters[metricKey{name: "conn_gateway_frames_dropped_total", labels: `reason="` + reason + `"`}] = 0
	}
	for _, name := range []string{"conn_gateway_conns", "conn_gateway_uds_connected"} {
		m.gauges[name] = 0
	}
	// 这两个是"观测值型"指标：没有内核连接 / 没有任何投递时是**未知**，不是 0。
	m.unobserved["conn_gateway_ack_lag_seconds"] = true
	m.unobserved["kernel_ack_lag_seconds"] = true
	return m
}

func (m *metricsRegistry) define(name, typ, help string) {
	m.mu.Lock()
	m.typ[name] = typ
	m.help[name] = help
	m.mu.Unlock()
}

// Inc 计数器 +delta（labels 形如 `reason="ring"`，空字符串表示无标签）。
func (m *metricsRegistry) Inc(name, labels string, delta float64) {
	m.mu.Lock()
	m.counters[metricKey{name, labels}] += delta
	m.mu.Unlock()
}

// Set 设置 gauge，并标记为"已观测"。
func (m *metricsRegistry) Set(name string, v float64) {
	m.mu.Lock()
	m.gauges[name] = v
	delete(m.unobserved, name)
	m.mu.Unlock()
}

// SetUnobserved 标记 gauge 尚未观测：渲染为 NaN（而不是 0）。
func (m *metricsRegistry) SetUnobserved(name string) {
	m.mu.Lock()
	m.unobserved[name] = true
	m.mu.Unlock()
}

// SetCounter 绝对覆盖无标签计数器（用于从 hub 快照同步的内部计数）。
func (m *metricsRegistry) SetCounter(name string, v float64) {
	m.SetCounterLabels(name, "", v)
}

// SetCounterLabels 绝对覆盖带标签计数器（用于按 reason 归因的丢弃计数）。
func (m *metricsRegistry) SetCounterLabels(name, labels string, v float64) {
	m.mu.Lock()
	m.counters[metricKey{name, labels}] = v
	m.mu.Unlock()
}

// Render 输出 Prometheus text exposition format（按 name + labels 排序，输出稳定便于 diff）。
func (m *metricsRegistry) Render(w io.Writer) {
	m.mu.Lock()
	counters := make(map[metricKey]float64, len(m.counters))
	for k, v := range m.counters {
		counters[k] = v
	}
	gauges := make(map[string]float64, len(m.gauges))
	for k, v := range m.gauges {
		gauges[k] = v
	}
	help := make(map[string]string, len(m.help))
	for k, v := range m.help {
		help[k] = v
	}
	typ := make(map[string]string, len(m.typ))
	for k, v := range m.typ {
		typ[k] = v
	}
	unobserved := make(map[string]bool, len(m.unobserved))
	for k, v := range m.unobserved {
		unobserved[k] = v
	}
	m.mu.Unlock()

	names := map[string]bool{}
	for k := range counters {
		names[k.name] = true
	}
	for n := range gauges {
		names[n] = true
	}
	// 未观测的 gauge 也要出现在输出里（渲染为 NaN），否则监控看到的是"指标缺失"而不是"未知"。
	for n := range unobserved {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		t := typ[name]
		if t == "" {
			t = "gauge"
		}
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help[name], name, t)
		keys := make([]metricKey, 0, len(counters))
		for k := range counters {
			if k.name == name {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].labels < keys[j].labels })
		for _, k := range keys {
			if k.labels == "" {
				_, _ = fmt.Fprintf(w, "%s %g\n", name, counters[k])
			} else {
				_, _ = fmt.Fprintf(w, "%s{%s} %g\n", name, k.labels, counters[k])
			}
		}
		if unobserved[name] {
			_, _ = fmt.Fprintf(w, "%s NaN\n", name)
			continue
		}
		if v, ok := gauges[name]; ok {
			_, _ = fmt.Fprintf(w, "%s %g\n", name, v)
		}
	}
}
