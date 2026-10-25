// v1.2.3：审批超时升级后台 goroutine。
package api

import (
	"log"
	"strconv"
	"time"
)

// StartApprovalEscalator 启动审批超时升级扫描（每 60 秒一次）。
func (d *Deps) StartApprovalEscalator() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			list, err := d.Engine.ListPending(0)
			if err != nil {
				continue
			}
			for _, t := range list {
				elapsed := time.Since(t.CreatedAt)
				topic := "approval:" + strconv.FormatUint(t.UserID, 10)
				switch {
				case elapsed > 2*time.Hour && t.Escalation < 2:
					log.Printf("[escalate] ticket=%s 2h+ urgent", t.ID)
					d.Stream.Broadcast(topic, map[string]any{
						"type": "escalation_urgent", "ticketId": t.ID, "elapsedMin": int(elapsed.Minutes()),
					})
				case elapsed > 30*time.Minute && t.Escalation < 1:
					log.Printf("[escalate] ticket=%s 30min watch push", t.ID)
					d.Stream.Broadcast(topic, map[string]any{
						"type": "escalation_watch", "ticketId": t.ID, "elapsedMin": int(elapsed.Minutes()),
					})
				}
			}
		}
	}()
}
