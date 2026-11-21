# DSH 会话：还原、导入、换机

## 当前状态（2026-11-21 迁移后）

| 项 | 值 |
| --- | --- |
| 工作区路径 | `E:\DSH\DS\IM2A` |
| 工作区 id | `39c1a6fe-db27-4ad5-a3d8-a03a5722cca2` |
| 还原后的根会话 | `session-4c497ceb-5de7-4c51-841b-8bdd4b279cf4` |
| 事件数 | 50,452（根）+ 117 个子代理会话 |
| 存储位置 | `C:\Users\fangw\.dsh\sessions\--E-DSH-DS-IM2A--\<id>\session.v4.jsonl.zstd` |
| 原会话（未改动） | `session-1ed0e3a7-85e8-4788-8710-6bc397ddf662`（仍在 `--E-GPT--` 下） |

还原采用「复制 + 换新 id」，**原会话原封不动**，两份可并存。

## 工具

`tools/session/`（零依赖，Node ≥ 22.15 提供 zstd）：

| 命令 | 作用 |
| --- | --- |
| `snapshot-session.mjs --from-dsh <id> --out sessions\current` | 把会话（含子代理）快照到项目目录 |
| `restore-session.mjs --snapshot <dir> --workspace <path>` | 从快照还原到工作区（新 id） |
| `restore-session.mjs --from-dsh <id> --workspace <path>` | 直接从 DSH 存储还原 |
| `import-session.mjs --zip <zip> --workspace <path>` | 导入官方会话导出 ZIP |
| `restore-session.mjs --attach-only --session-id <id> --workspace <path>` | 仅补挂载（DSH 重启后） |

## 换机后恢复本会话

```powershell
cd E:\DSH\DS\IM2A
node tools/session/restore-session.mjs --snapshot sessions\current --workspace E:\DSH\DS\IM2A
```

DSH 运行时该命令会**跳过挂载**（打印 `attach.skipped`），因为宿主把注册表放在内存里。
此时：关闭 DSH → 重新执行一次（或加 `--attach-only`）→ 重开 DSH。

## 导入官方导出 ZIP

DSH 菜单「下载 Session 日志」产出的 `dsh-session-*.zip`：

```powershell
node tools/session/import-session.mjs --zip D:\path\dsh-session-xxx.zip --workspace E:\DSH\DS\IM2A
```

- ZIP 结构：`session.v4.jsonl` + `subagents/<id>/session.v4.jsonl` + `media/` + `files/`；
- 导入分配新 id；媒体落 `attachments/v1/objects/<ab>/<digest>`，普通文件落 `files/`；
- 已验证：合成 ZIP（root + 子代理 + 1 媒体 + 1 附件）在隔离 DSH_HOME 中完整导入并自动挂载。

## 坑

1. **不要**在 DSH 运行时手改 `C:\Users\fangw\.dsh\storages\workspace.json`——宿主会用内存态覆盖。
2. 同一 session id 不能同时出现在两个 project 目录，否则整条读取失败；重新导入一律用新 id。
3. `header.cwd` 必须与磁盘 project 目录名一致，改 cwd 必须同时移动目录。
4. `session_projcache` 是派生缓存，删掉即可，不要手改。
5. 快照是字节复制，会话正在写入时可能截断最后一帧；还原报解码错误时重新快照一次。
