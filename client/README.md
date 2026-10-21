# im2agent 客户端（Flutter · iOS / Android / Windows）

> **本机无 Flutter SDK，未编译验证**（`flutter analyze` 与真机运行都未执行）。
> 代码按 Flutter 3.19 / Dart 3.3 写，接口对齐 `apps/im2agent/server.mjs`。

## 它演示什么

三态导航，全部连真实后端（不是 mock）：

| Tab | 接口 | 说明 |
|---|---|---|
| **审批** | `GET /api/admin/state` + `POST /api/approval/decide` | 待审批卡片、通过/驳回；下方显示付费消息状态 |
| **账本** | `GET /api/ledger` | 不可变账本条目与统计（幂等键可见，便于对账） |
| **审计** | `GET /api/audit` | 哈希链是否完整 + 最近事件 |

## 怎么跑

```bash
cd client
flutter pub get

# 1) 先起后端（默认 demo，127.0.0.1:8080 需改为本机可访问地址）
cd ..
PROFILE=lan ADMIN_TOKEN=dev-token-123 PORT=8080 node apps/im2agent/server.mjs

# 2) 三端运行（连接信息可在首屏输入，也可用 --dart-define）
flutter run -d windows --dart-define=BASE_URL=http://127.0.0.1:8080 --dart-define=ADMIN_TOKEN=dev-token-123
flutter run -d android --dart-define=BASE_URL=http://192.168.1.10:8080 --dart-define=ADMIN_TOKEN=dev-token-123
flutter run -d ios     --dart-define=BASE_URL=http://192.168.1.10:8080 --dart-define=ADMIN_TOKEN=dev-token-123
```

> 真机连局域网时注意：Android 9+ 默认禁止明文 HTTP，需要在 `AndroidManifest.xml` 加
> `android:usesCleartextTraffic="true"`（仅调试）或改用 HTTPS；iOS 需在 `Info.plist` 配 ATS 例外。
> 生产（公网）走 HTTPS 域名，不需要这些例外。

## 打包命令

```bash
# Windows
flutter build windows --release

# Android（App Bundle 上架用）
flutter build appbundle --release

# iOS（需 macOS + Xcode + 签名）
flutter build ipa --release
```

## 已知边界

- 未做本地持久化（token 未落 `shared_preferences`，重启需重输；`pubspec` 已声明依赖，接一行即可）。
- 未做推送/离线，审批更新靠下拉刷新。
- 不做 IM 聊天界面：本原型的客户端定位是**运维/审批端**，聊天面在网页端（`apps/im-live`）。
