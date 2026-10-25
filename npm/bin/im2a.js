#!/usr/bin/env node
'use strict';
// im2a: IM2A gateway 跨平台启动器。
// 优先级：IM2A_BIN 环境变量（开发/测试）> 本地缓存 > 从 GitHub Release 下载。
const os = require('os');
const fs = require('fs');
const path = require('path');
const https = require('https');
const { spawn } = require('child_process');
const { execFileSync } = require('child_process');

const PKG_VERSION = '2.0.2';
const REPO = 'TwinsEarth/im2agent';

function platformAsset() {
  const goos = process.platform === 'darwin' ? 'darwin'
    : process.platform === 'win32' ? 'windows' : 'linux';
  const goarch = process.arch === 'arm64' ? 'arm64'
    : process.arch === 'x64' ? 'amd64' : null;
  if (!goarch) throw new Error('unsupported arch: ' + process.arch);
  const ext = goos === 'windows' ? '.exe' : '';
  return `im2a-gateway_${goos}_${goarch}${ext}`;
}

function cacheDir() {
  const d = path.join(os.homedir(), '.im2a', 'bin', PKG_VERSION);
  fs.mkdirSync(d, { recursive: true });
  return d;
}

function download(url, dest) {
  return new Promise((resolve, reject) => {
    const follow = (u) => https.get(u, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        return follow(res.headers.location);
      }
      if (res.statusCode !== 200) {
        return reject(new Error('download failed HTTP ' + res.statusCode + ' ' + u));
      }
      const tmp = dest + '.part';
      const out = fs.createWriteStream(tmp);
      res.pipe(out);
      out.on('finish', () => { out.close(() => { fs.renameSync(tmp, dest); resolve(); }); });
      out.on('error', reject);
    }).on('error', reject);
    follow(url);
  });
}

async function resolveBinary() {
  const asset = platformAsset();
  if (process.env.IM2A_BIN) {
    if (!fs.existsSync(process.env.IM2A_BIN)) throw new Error('IM2A_BIN not found: ' + process.env.IM2A_BIN);
    return process.env.IM2A_BIN;
  }
  const cached = path.join(cacheDir(), asset);
  if (fs.existsSync(cached)) return cached;
  const url = `https://github.com/${REPO}/releases/download/v${PKG_VERSION}/${asset}`;
  process.stderr.write(`[im2a] downloading ${asset} from ${url}\n`);
  await download(url, cached);
  if (process.platform !== 'win32') fs.chmodSync(cached, 0o755);
  return cached;
}

(async () => {
  try {
    const args = process.argv.slice(2);
    // --im2a-version 只打印启动器与二进制版本（自检/冒烟，不下载）
    if (args.includes('--im2a-version')) {
      process.stdout.write(`im2a launcher v${PKG_VERSION} (asset ${platformAsset()})\n`);
      return;
    }
    const bin = await resolveBinary();
    const child = spawn(bin, args, { stdio: 'inherit' });
    child.on('exit', (code) => process.exit(code || 0));
    child.on('error', (e) => { process.stderr.write(String(e.message || e) + '\n'); process.exit(1); });
  } catch (e) {
    process.stderr.write('[im2a] ' + (e.message || e) + '\n');
    process.exit(1);
  }
})();
