# im2agent 局域网镜像
# 说明：Node 单进程（零依赖），不是 Go 静态二进制；镜像体积以 node:alpine 为准。
FROM node:24-alpine

WORKDIR /app

# 只复制运行所需（本原型无 npm 依赖，不需要 install）
COPY apps/im2agent ./apps/im2agent
COPY services ./services

ENV PROFILE=lan \
    PORT=8080 \
    HOST=0.0.0.0 \
    STORE=jsonfile \
    STORE_FILE=/data/state.json \
    NODE_ENV=production

RUN mkdir -p /data && addgroup -S im2agent && adduser -S im2agent -G im2agent && chown -R im2agent:im2agent /data /app
USER im2agent

EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --retries=5 --start-period=5s \
  CMD node -e "fetch('http://127.0.0.1:8080/api/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"

CMD ["node", "apps/im2agent/server.mjs"]
