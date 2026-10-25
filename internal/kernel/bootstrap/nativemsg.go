// SPDX-License-Identifier: MIT
//
// C3：native.MessagePublisher 的 bootstrap 适配器。
//
// 语义：把一条已提交消息（payload 为自包含 JSON）投递给某 recipient，内部走
// connapi.Bridge.Publish —— 即复用 B2-b 的投递序号/日志/位点/补发，**不另造投递游标**。
// critical=true：业务消息不可丢（网关侧慢消费优先保留；日志已持久化可补发）。
package bootstrap

import (
	"context"
	"time"

	"human-agent-im/internal/connapi"
)

// bridgeMessagePublisher 用 connapi.Bridge 实现 native.MessagePublisher。
type bridgeMessagePublisher struct {
	bridge  *connapi.Bridge
	timeout time.Duration
}

// PublishMessage 投递一条消息给 recipient（写 im_delivery_log + UDS 投递）。
func (p *bridgeMessagePublisher) PublishMessage(ctx context.Context, recipientUserID int64, payload []byte, critical bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	_, err := p.bridge.Publish(ctx, recipientUserID, []connapi.PublishFrame{{
		Payload:  payload,
		Critical: critical,
	}})
	return err
}
