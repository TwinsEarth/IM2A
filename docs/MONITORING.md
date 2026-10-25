# IM2A 监控告警（v1.9.9）

## 指标端点
- `GET /metrics` 与 `GET /healthz` **均要求 admin token 鉴权**（`Authorization: Bearer $IM2A_ADMIN_TOKEN`），无 token 返回 401。输出 Prometheus text exposition format。
- 已真实计数：`http_requests_total{path,code}` 计数器 + `http_request_duration_ms` summary（count/avg/p99）。

## Prometheus 抓取配置
```yaml
scrape_configs:
  - job_name: im2a-gateway
    metrics_path: /metrics
    bearer_token: "${IM2A_ADMIN_TOKEN}"   # v1.9.9：/metrics 已鉴权
    static_configs:
      - targets: ["127.0.0.1:8080"]
```

## Grafana 面板建议
1. 请求 QPS：`sum(rate(http_requests_total[1m])) by (path)`
2. 错误率：`sum(rate(http_requests_total{code=~"5..|4.."}[5m])) / sum(rate(http_requests_total[5m]))`
3. 延迟 p99：`http_request_duration_ms_p99_ms`

## 告警规则示例
```yaml
groups:
- name: im2a
  rules:
  - alert: HighErrorRate
    expr: sum(rate(http_requests_total{code=~"5.."}[5m])) / sum(rate(http_requests_total[5m])) > 0.02
    for: 2m
    labels: { severity: critical }
    annotations: { summary: "5xx 错误率超过 2%" }
  - alert: HighLatency
    expr: http_request_duration_ms_p99_ms > 500
    for: 5m
    labels: { severity: warning }
    annotations: { summary: "p99 延迟超过 500ms" }
```

## 验证
- `curl -s http://127.0.0.1:8080/metrics` 应返回 `# TYPE http_requests_total counter`。
- 真实单测：`go test ./internal/metrics/ -count=1`（3/3 PASS）。
