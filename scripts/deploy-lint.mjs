#!/usr/bin/env node
/**
 * 模块职责：部署产物的静态检查（③ Linux / ⑤ LAN / ⑦ PROD）
 * 契约来源：deploy/linux/README.md 的验收清单；deploy/prod 的加固要求
 *
 * 为什么需要它：本机沙箱禁止命名管道，Git Bash 起不来，`bash -n` 跑不了；
 * 而 Docker/Caddy 也不在本机。于是把"部署脚本里必须有/必须没有的东西"变成可执行的静态断言，
 * 至少保证**交付出去的脚本不会因为漏了 chmod/凭证/健康检查而在目标机上炸**。
 * 它不能替代目标机实测——这一点在最后一行明确输出。
 */
import { readFileSync, existsSync } from 'node:fs';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const results = [];
const read = (rel) => readFileSync(path.join(ROOT, rel), 'utf8');
const check = (name, ok, detail = '') => results.push({ name, ok, detail });

const REQUIRED = [
  'deploy/linux/install.sh',
  'deploy/linux/im2agent.service',
  'deploy/linux/README.md',
  'deploy/lan/Dockerfile',
  'deploy/lan/docker-compose.yml',
  'deploy/prod/Caddyfile',
  'deploy/prod/docker-compose.yml',
  'deploy/prod/.env.example'
];
const missing = REQUIRED.filter((rel) => !existsSync(path.join(ROOT, rel)));
check(`部署文件齐备（${REQUIRED.length} 个）`, missing.length === 0, missing.join(', '));

// ③ Linux 安装脚本
{
  const text = read('deploy/linux/install.sh');
  const must = [
    ['#!/usr/bin/env bash', 'shebang'],
    ['set -euo pipefail', '严格模式（出错即停）'],
    ['--dry-run', '演练模式'],
    ['NODE_MAJOR', 'Node 版本检查'],
    ['ADMIN_TOKEN', '操作者凭证'],
    ['VIEWER_TOKEN', '只读凭证'],
    ['chmod 600', '凭证文件权限'],
    ['systemctl enable', '开机自启'],
    ['/healthz', '安装后健康检查']
  ];
  const absent = must.filter(([needle]) => !text.includes(needle)).map(([, label]) => label);
  check(`install.sh 必备项（${must.length} 项）`, absent.length === 0, absent.join(', '));
  check('install.sh 不在 dry-run 时强制 root', text.includes('id -u') && text.includes('DRY_RUN" -eq 0'), '缺 root 校验');
}

// ③ systemd 单元
{
  const text = read('deploy/linux/im2agent.service');
  const must = ['[Unit]', '[Service]', '[Install]', 'ExecStart=/usr/bin/node apps/im2agent/server.mjs', 'EnvironmentFile=/etc/im2agent/im2agent.env', 'Restart=always', 'ProtectSystem=strict', 'NoNewPrivileges=true'];
  const absent = must.filter((needle) => !text.includes(needle));
  check('systemd 单元关键指令', absent.length === 0, absent.join(', '));
}

// ⑤ LAN 镜像
{
  const dockerfile = read('deploy/lan/Dockerfile');
  const compose = read('deploy/lan/docker-compose.yml');
  const dfMust = ['FROM node:', 'USER im2agent', 'HEALTHCHECK', 'EXPOSE', 'CMD ['];
  const dfAbsent = dfMust.filter((needle) => !dockerfile.includes(needle));
  check('Dockerfile 关键指令', dfAbsent.length === 0, dfAbsent.join(', '));
  check('Dockerfile 使用非 root 用户', /USER\s+im2agent/.test(dockerfile), '缺 USER');
  const yamlFiles = [['deploy/lan/docker-compose.yml', compose], ['deploy/prod/docker-compose.yml', read('deploy/prod/docker-compose.yml')]];
  const tabs = yamlFiles.filter(([, text]) => text.includes('\t')).map(([rel]) => rel);
  check('compose 文件不含 Tab（YAML 缩进用空格）', tabs.length === 0, tabs.join(', '));
  check('LAN compose 有健康检查与端口映射', compose.includes('healthcheck') && compose.includes('ports'), '缺 healthcheck/ports');
}

// ⑦ 生产形态
{
  const caddy = read('deploy/prod/Caddyfile');
  const must = ['reverse_proxy', 'header', 'Strict-Transport-Security', 'X-Frame-Options', 'X-Content-Type-Options', 'encode'];
  const absent = must.filter((needle) => !caddy.includes(needle));
  check('Caddyfile 关键加固项', absent.length === 0, absent.join(', '));

  const compose = read('deploy/prod/docker-compose.yml');
  const env = read('deploy/prod/.env.example');
  check('生产 compose 强制 ADMIN_TOKEN（缺失即失败）', compose.includes('ADMIN_TOKEN: ${ADMIN_TOKEN:?'), '缺 ${ADMIN_TOKEN:?}');
  check('生产 compose 有 caddy 服务与持久卷', compose.includes('caddy:') && compose.includes('caddy-data'), '缺 caddy/卷');
  check('.env.example 覆盖凭证/存储/LLM/PG 占位', ['ADMIN_TOKEN', 'STORE', 'IM2AGENT_LLM_KEY', 'POSTGRES_URL'].every((key) => env.includes(key)), '缺占位项');
  check('生产说明包含 TRUST_PROXY 提醒', read('deploy/linux/README.md').includes('TRUST_PROXY=1'), '反代后限流失效的风险未说明');
}

// 代码与部署一致性：部署启动的入口必须存在
{
  const service = read('deploy/linux/im2agent.service');
  const match = service.match(/ExecStart=\S+\s+(\S+)/);
  const entry = match?.[1] ?? null;
  check('systemd 指向的入口文件存在', entry !== null && existsSync(path.join(ROOT, entry)), `entry=${entry}`);
}

const passed = results.filter((item) => item.ok).length;
console.log(`\n部署产物静态检查：${passed}/${results.length} 通过\n`);
for (const item of results) {
  console.log(`${item.ok ? '✔' : '✘'} ${item.name}${item.ok || !item.detail ? '' : `\n    ↳ ${item.detail}`}`);
}
console.log('\n⚠ 静态检查 ≠ 目标机实测：bash/systemd/docker/caddy 均未在本机执行，需按 deploy/linux/README.md 验收。');

if (passed !== results.length) {
  process.exitCode = 1;
}
