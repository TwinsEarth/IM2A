// Package id 提供不依赖外部模块的 UUID v4 生成（crypto/rand）。
// 付费消息 message_id 等业务幂等键需要全局唯一（§4.2 / §10-R2）。
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// UUIDv4 生成一个 RFC 4122 v4 UUID 字符串（小写，含连字符）。
func UUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，属于不可恢复的环境错误。
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	// version 4 与 variant 10xx
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
