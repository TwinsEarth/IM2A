package openclaw

import (
	"errors"
	"testing"
	"time"
)

// C1 错误分类：token 失效 / 限流 / 正常输出。
func TestClassifySendError(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want error
	}{
		{"errcode-14", `{"errcode":-14,"errmsg":"context token expired"}`, ErrTokenExpired},
		{"token-expired-en", "Error: token expired please relogin", ErrTokenExpired},
		{"login-expired-cn", "登录已失效，请重新登录", ErrTokenExpired},
		{"429", `{"errcode":429,"errmsg":"too many requests"}`, ErrRateLimited},
		{"rate-limit-cn", "触发限流，请稍后再试", ErrRateLimited},
		{"normal-ok", `{"deliveryStatus":"sent","messageId":"abc"}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySendError([]byte(c.out))
			if c.want == nil {
				if got != nil {
					t.Fatalf("expect nil, got %v", got)
				}
				return
			}
			if !errors.Is(got, c.want) {
				t.Fatalf("expect %v, got %v", c.want, got)
			}
		})
	}
}

// C2 退避计划：5 档递增。
func TestBackoffPlan(t *testing.T) {
	if len(backoffPlan) != 5 {
		t.Fatalf("expect 5 backoff steps, got %d", len(backoffPlan))
	}
	for i := 1; i < len(backoffPlan); i++ {
		if backoffPlan[i] <= backoffPlan[i-1] {
			t.Fatalf("backoff not increasing at %d: %v", i, backoffPlan)
		}
	}
}

// C3 熔断：pause 后 IsPaused=true，解除后 false。
func TestCircuitBreaker(t *testing.T) {
	acc := "probe_breaker_unit"
	pauseMu.Lock()
	delete(pausedUntil, acc)
	pauseMu.Unlock()
	if IsPaused(acc) {
		t.Fatal("should not be paused before pause")
	}
	pauseAccount(acc)
	if !IsPaused(acc) {
		t.Fatal("should be paused after pauseAccount")
	}
	pauseMu.Lock()
	delete(pausedUntil, acc)
	pauseMu.Unlock()
	if IsPaused(acc) {
		t.Fatal("should not be paused after clearing")
	}
}

// D4 并发上限：信号量容量为 10。
func TestSendSemaphoreCapacity(t *testing.T) {
	if cap(sendSem) != 10 {
		t.Fatalf("expect sendSem cap 10, got %d", cap(sendSem))
	}
}

// C4 outbox 缓冲：容量 1024，避免短时丢失。
func TestOutboxBuffer(t *testing.T) {
	if cap(outbox) != 1024 {
		t.Fatalf("expect outbox cap 1024, got %d", cap(outbox))
	}
}

// 熔断时长默认 10 分钟。
func TestPauseDuration(t *testing.T) {
	if pauseDuration != 10*time.Minute {
		t.Fatalf("expect 10m pause, got %v", pauseDuration)
	}
}
