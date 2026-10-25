# IM2A GA 私有化部署手册（v1.9.5）

适用对象：把 IM2A 全栈（网关 + PostgreSQL + Redis + Kafka）部署到一台私有化服务器，离线/内网可复现。

## 1. 前置要求

| 项 | 最低 | 推荐 |
|---|---|---|
| CPU / 内存 | 2 核 / 4 GB | 4 核 / 8 GB |
| 磁盘 | 20 GB | 50 GB SSD |
| OS | Debian 12 / Ubuntu 22.04 | 同左 |
| Docker | 24+（含 compose 插件） | 同左 |

> OpenSearch 默认不启动（约占 1 GB 内存）。小机器无需开启；需要全文检索时再在 compose 中取消注释。

## 2. 一键启动

```bash
git clone https://github.com/TwinsEarth/im2agent.git && cd im2agent
cp deploy/.env.example deploy/.env
$EDITOR deploy/.env          # 必改：IM2A_JWT_SECRET / IM2A_ADMIN_TOKEN / PG_PASSWORD

# 生成强随机密钥：
#   openssl rand -hex 32

docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml up -d
```

首次启动 PostgreSQL 会按 `deploy/migrations/*.sql` 顺序初始化空库；已有 v0.2 之后的迁移（含 016_auth / 017_encrypted_context）需在库就绪后手动应用：

```bash
docker exec -i im2a-postgres psql -U hai -d hai_platform < internal/store/migrations/016_auth.sql
docker exec -i im2a-postgres psql -U hai -d hai_platform < internal/store/migrations/017_encrypted_context.sql
```

## 3. 验证

```bash
curl -s http://127.0.0.1:8080/healthz
# {"status":"ok","version":"v1.9.5",...}

# 登录签发 JWT（identifier 任意，首次自动落库）
curl -s -X POST http://127.0.0.1:8080/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"identifier":"owner"}'
```

## 4. 容器与端口

| 容器 | 端口 | 作用 |
|---|---|---|
| im2a-gateway | 8080 | 统一 API 网关（全部路由入口） |
| im2a-postgres | 5432 | 关系库 / pgvector 列 / 审计 |
| im2a-redis | 6379 | 缓存 / 限流计数 |
| im2a-kafka | 9092 | 审批与审计事件流 |

生产暴露 8080 建议前置 nginx + TLS（参考 `deploy/https-setup.sh <域名>`）。

## 5. 安全清单（上线前逐项确认）

- [ ] `IM2A_JWT_SECRET` 已改为 `openssl rand -hex 32`（**未改则登录态可被伪造**）
- [ ] `IM2A_ADMIN_TOKEN` 已改为强随机值
- [ ] `PG_PASSWORD` 已改且不使用默认 `hai_dev`
- [ ] 8080 不直接对公网开放，走 nginx/TLS 或内网
- [ ] 定期备份 pg_data 卷（`docker run --rm -v im2agent_pg_data ...`）

## 6. 运维常用命令

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml logs -f gateway
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml restart gateway
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml down          # 停服务，保留数据卷
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml down -v      # 危险：连数据卷一起删
```

## 7. 离线/内网说明

- 所有镜像 tag 固定（postgres:16-alpine、redis:7-alpine、apache/kafka:3.8.0），无 `latest` 漂移。
- 离线主机：在有网机器 `docker compose ... pull && docker save | gzip > im2a-images.tgz`，内网 `docker load < im2a-images.tgz`。
- Gateway 镜像多阶段构建后为纯静态二进制（alpine + 无源码），不依赖外网运行。
