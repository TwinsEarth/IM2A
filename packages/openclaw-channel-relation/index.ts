// SPDX-License-Identifier: MIT
//
// IM2A Relation（OpenClaw 频道插件）入口。
//
// 注意：本文件是 OpenClaw 的 `openclaw.extensions` 入口（宿主按包格式加载），真正的运行时
// 实现编译产物在 `dist/src/index.js`（`openclaw.runtimeExtensions`）。发布前 `npm pack`
// 会通过 `prepack` 自动构建 dist。
export { default } from "./dist/src/index.js";
export * from "./dist/src/index.js";
