// SPDX-License-Identifier: MIT

package pluginruntime

import _ "embed"

// schemaJSON 是 manifest v1 JSON Schema 的唯一副本；构建期随包嵌入，
// CI/运维可直接取用（不依赖仓库路径），测试会断言它与 Go 结构体字段一致。
//
//go:embed schema/plugin-manifest-v1.schema.json
var schemaJSON []byte

// SchemaJSON 返回 manifest v1 JSON Schema 的原始字节（调用方不应修改返回值）。
func SchemaJSON() []byte { return schemaJSON }
