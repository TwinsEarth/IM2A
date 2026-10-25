// Package version 统一发布版本，所有服务 /healthz 与 Release 资产共用单一来源。
package version

// Version 为当前发布版本（语义化，带 v 前缀），由发布流程打 tag 前统一收口。
const Version = "v2.0.2"

// GitCommit 由构建期 -ldflags -X 注入；未注入时为 dev。
var GitCommit = "dev"
