// 模块职责：persistence 服务 barrel（文件级备份与恢复演练）
// 契约来源：contracts/INTERFACES.md §44（v0.0.5 新增）
// 关键不变量：restore 前必须验 sha256；restore 只返回数据、不自动应用。
export {
  BACKUP_CODE,
  BackupError,
  BackupService,
} from './backup.mjs';
