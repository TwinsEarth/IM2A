package api

import (
	"context"

	"human-agent-im/internal/eventbus"
	"human-agent-im/internal/model"
)

// modelBusAdapter 把 eventbus 适配为 model.ModelEventBus。
type modelBusAdapter struct {
	bus *eventbus.Bus
}

// PublishModelCall 实现 model.ModelEventBus。
func (a *modelBusAdapter) PublishModelCall(agentID string, payload map[string]any) {
	_ = a.bus.Publish(context.Background(), &eventbus.Event{
		Type:    eventbus.EventTypeModelCall,
		AgentID: agentID,
		Payload: payload,
	})
}

// 编译期接口检查
var _ model.ModelEventBus = (*modelBusAdapter)(nil)
