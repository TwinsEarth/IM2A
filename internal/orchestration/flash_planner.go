package orchestration

import (
	"context"
	"errors"

	"human-agent-im/internal/model"
)

var ErrNoPlanner = errors.New("orchestration: flash planner not configured")

// v2.3.4 A3/A4：把 Commander 真正接到 flash 模型（OpenAI-compatible chat）。
//
// 设计：
//   - FlashPlanner 实现 Planner 接口，把 userText + recentSummary 拼成单轮 user message，
//     systemPrompt 用 commander.go 里那份管家指令。
//   - 温度 0、maxTokens 256、超时由 Commander 的 6s ctx 控制。
//   - 任何错误/空返回/JSON 解析失败都由 Commander.Plan 安全降级。
//   - 不直接调用 relay/openclaw，Commander 只产出 Plan，不执行。

type FlashPlanner struct {
	cli model.Provider
}

func NewFlashPlanner(cli model.Provider) *FlashPlanner { return &FlashPlanner{cli: cli} }

func (f *FlashPlanner) Plan(ctx context.Context, userText, recentSummary string) (string, error) {
	if f == nil || f.cli == nil {
		return "", ErrNoPlanner
	}
	userMsg := userText
	if recentSummary != "" {
		userMsg = "近期对话摘要：" + recentSummary + "\n用户现在说：" + userText
	}
	resp, err := f.cli.Chat(ctx, &model.ChatRequest{
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		},
		Temperature: 0,
		MaxTokens:   256,
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
