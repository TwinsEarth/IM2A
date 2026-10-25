# IM2A Gateway 多阶段构建（私有化/离线全栈）
# 构建：docker build -f deploy/Dockerfile \
#         --build-arg VERSION="$(bash scripts/version.sh plain)" \
#         -t im2a-gateway:"$(bash scripts/version.sh semver)" .
# 版本号单一源见 EDITIONS.md；本文件不硬编码版本默认值。
FROM golang:1.27-alpine AS build
# 国内构建环境（生产服务器直连 proxy.golang.org 不通）
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct
WORKDIR /src
# 依赖先拷贝以利用层缓存
COPY go.mod ./
COPY go.sum* ./
RUN go mod download
COPY . .
ARG VERSION
RUN test -n "$VERSION" || (echo "ERROR: --build-arg VERSION=vX.Y.Z is required (see EDITIONS.md)" >&2; exit 1)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X human-agent-im/internal/version.Version=${VERSION}" \
    -o /out/im2a-gateway ./services/gateway

# 运行时：最小镜像（无 git/源码）
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 im2a
WORKDIR /app
COPY --from=build /out/im2a-gateway /app/im2a-gateway
USER im2a
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=12 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/im2a-gateway"]
