GO ?= go
# 版本单一源：core 只在 internal/version/version.go；edition 版本只在 editions.json。
# 不要在这里写死版本号；派生文件的同步与校验见 make version-sync / make version-check（EDITIONS.md）。
VERSION := $(shell bash scripts/version.sh plain)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X human-agent-im/internal/version.Version=$(VERSION) -X human-agent-im/internal/version.GitCommit=$(COMMIT)
SERVICES := msg-svc relation-svc approval-engine negotiation-svc payment-svc compliance-gate agent-orchestrator gateway

.PHONY: build vet selftest-all gotest test gate proto up down clean help \
        version-sync version-check versions binary-guard license-check hygiene

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# ---- 版本治理（core 单一源 + edition 矩阵，见 EDITIONS.md）----
version-sync: ## 从 version.go + editions.json 生成全部派生版本号
	bash scripts/version.sh sync

version-check: ## 版本单一源校验（任一漂移 → 非 0）
	bash scripts/version.sh check

versions: ## 打印 core + editions 版本矩阵（Markdown）
	bash scripts/version.sh table

binary-guard: ## 拒绝 >1MB 的非文本文件入库（防二进制回归）
	bash scripts/check-no-binaries.sh

license-check: ## LICENSE 与 npm/插件包 license 字段一致性
	bash scripts/check-license.sh

hygiene: version-check binary-guard license-check ## 仓库卫生门禁（版本/二进制/许可）

build: ## 编译全部服务
	$(GO) build ./...

vet: ## 静态检查
	$(GO) vet ./...

gotest: ## 运行 Go 单元/端到端测试（含竞态检测）
	$(GO) test -race ./...

selftest-all: ## 运行全部服务自测（验证核心机制）
	@for s in $(SERVICES); do \
		echo "===== $$s ====="; \
		$(GO) run ./services/$$s -selftest || exit 1; \
	done

test: gate ## 全量关卡（版本/二进制门禁 + build+vet+race 测试+服务自测）

gate: hygiene build vet gotest selftest-all ## 发布前全量关卡：版本/卫生 → 编译 / vet / -race 测试 / 7 服务自测

run: ## 启动单个服务（示例：make run SVC=agent-orchestrator PORT=8080）
	$(GO) run ./services/$(SVC) -port $(PORT)

gateway: ## 启动统一网关（示例：make gateway PORT=8080）
	$(GO) run ./services/gateway -port $(PORT)

proto: ## 生成 Protobuf 代码（需 protoc + protoc-gen-go + protoc-gen-go-grpc）
	@command -v protoc >/dev/null 2>&1 || (echo "protoc 未安装：brew install protobuf"; exit 1)
	@command -v protoc-gen-go >/dev/null 2>&1 || (echo "安装：go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"; exit 1)
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 || (echo "安装：go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest"; exit 1)
	mkdir -p proto/gen
	protoc -I proto --go_out=paths=source_relative:proto/gen --go-grpc_out=paths=source_relative:proto/gen proto/*.proto

up: ## 启动基础依赖（postgres/redis/kafka/etcd/opensearch）
	docker compose -f deploy/docker-compose.yml up -d

down: ## 停止基础依赖
	docker compose -f deploy/docker-compose.yml down

clean: ## 清理生成物
	rm -rf proto/gen bin dist data logs

# ---- 发布 ----
.PHONY: release-build checksums compat

release-build: ## 跨平台编译网关二进制（linux/darwin/windows × amd64/arm64）
	@mkdir -p dist
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			out="dist/im2a-gateway_$${os}_$${arch}"; \
			[ "$$os" = windows ] && out="$$out.exe"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" ./services/gateway; \
			echo "BUILT $$out"; \
		done; \
	done
	@$(MAKE) checksums

checksums: ## 生成 dist 校验和
	@cd dist && sha256sum im2a-gateway* > checksums.txt && cat checksums.txt

compat: ## 兼容性门禁：契约稳定 + 跨平台矩阵编译
	@bash scripts/compat-check.sh
