package main

// metrics_test.go：指标"未知 vs 零"语义的回归测试（lead 评审要求）。
//
// 背景：`conn_gateway_ack_lag_seconds` / `kernel_ack_lag_seconds` 在"尚未观测"时若输出 0，
// 监控会把"没数据"误判为"零延迟/健康"。约定：未观测 → `NaN`；已观测后 0 才是真 0。
// 而 `frames_dropped_total{reason=...}` 的 0 是**可观测的零**（真的没丢），必须照常输出。

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetricsUnobservedGaugesRenderNaN(t *testing.T) {
	m := newMetricsRegistry()
	var buf bytes.Buffer
	m.Render(&buf)
	out := buf.String()

	if !strings.Contains(out, "conn_gateway_ack_lag_seconds NaN") {
		t.Fatalf("未观测的 ack lag 应输出 NaN，实际：\n%s", out)
	}
	if !strings.Contains(out, "kernel_ack_lag_seconds NaN") {
		t.Fatalf("未观测的 kernel ack lag 应输出 NaN，实际：\n%s", out)
	}
	// "没发生丢弃" = 可观测的 0，必须照常输出（否则会被误判为指标缺失）
	for _, reason := range []string{"ring", "shard", "ackq", "uds"} {
		if !strings.Contains(out, `conn_gateway_frames_dropped_total{reason="`+reason+`"} 0`) {
			t.Fatalf("丢弃原因 %q 的 0 值应输出，实际：\n%s", reason, out)
		}
	}
	// 连接数/内核连接数是真的 0
	if !strings.Contains(out, "conn_gateway_conns 0") || !strings.Contains(out, "conn_gateway_uds_connected 0") {
		t.Fatalf("conns/uds_connected 应输出 0：\n%s", out)
	}
}

func TestMetricsObservedValuesReplaceNaN(t *testing.T) {
	m := newMetricsRegistry()
	m.Set("conn_gateway_ack_lag_seconds", 0.25)
	m.Set("kernel_ack_lag_seconds", 0) // 已观测的 0（内核报告健康）也必须是数字

	var buf bytes.Buffer
	m.Render(&buf)
	out := buf.String()
	if !strings.Contains(out, "conn_gateway_ack_lag_seconds 0.25") {
		t.Fatalf("观测值未生效：\n%s", out)
	}
	if !strings.Contains(out, "kernel_ack_lag_seconds 0\n") {
		t.Fatalf("已观测的 0 应输出数字 0：\n%s", out)
	}
	if strings.Contains(out, "kernel_ack_lag_seconds NaN") {
		t.Fatalf("已观测后不应再是 NaN：\n%s", out)
	}
}

func TestMetricsSetUnobservedRestoresNaN(t *testing.T) {
	m := newMetricsRegistry()
	m.Set("conn_gateway_ack_lag_seconds", 1.5)
	m.SetUnobserved("conn_gateway_ack_lag_seconds")
	var buf bytes.Buffer
	m.Render(&buf)
	if !strings.Contains(buf.String(), "conn_gateway_ack_lag_seconds NaN") {
		t.Fatalf("SetUnobserved 后应回到 NaN：\n%s", buf.String())
	}
}
