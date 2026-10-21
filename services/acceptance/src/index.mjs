// 模块职责：acceptance 服务 barrel（上线前预检 + 验收执行器）
// 契约来源：contracts/INTERFACES.md §45–§46（v0.0.6 新增）
// 关键不变量：预检只读；不可自动化显式 MANUAL；异常记 FAIL 且不中断整轮。
export { PREFLIGHT_CODE, PreflightError, runPreflight } from './preflight.mjs';
export { GATE_STATUS, AcceptanceRunner, DEFAULT_GATES } from './runner.mjs';
