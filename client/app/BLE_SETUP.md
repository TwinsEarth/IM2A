# v1.4.3 真实 BLE 接入（flutter_blue_plus）

Flutter 端已实现**真实 BLE**（非模拟）：`lib/services/ble/`。

## 代码结构
| 文件 | 职责 |
|---|---|
| `ble_gatt.dart` | GATT Service/Characteristic UUID、设备类型、命令字（与 Go `internal/edge/ble.go` 对齐） |
| `ble_device.dart` | 设备模型、上行事件解析 |
| `ble_service.dart` | 真实扫描 / 连接 / 服务发现 / notify 订阅 / write 下发 |
| `screens/devices_screen.dart` | 设备管理页（扫描、连接、电量、信号、断开） |

## 首次运行
\`\`\`bash
cd client/app
flutter create .          # 生成 android/ios/desktop 平台壳
flutter pub get
flutter run
\`\`\`

## 权限配置（flutter create 后必须加）

### Android — android/app/src/main/AndroidManifest.xml
\`\`\`xml
<uses-permission android:name="android.permission.BLUETOOTH_SCAN"
    android:usesPermissionFlags="neverForLocation" />
<uses-permission android:name="android.permission.BLUETOOTH_CONNECT" />
<uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />
\`\`\`

### iOS — ios/Runner/Info.plist
\`\`\`xml
<key>NSBluetoothAlwaysUsageDescription</key>
<string>连接耳机/眼镜/指环，接收唤醒与审批指令（原始音频不离开设备）</string>
\`\`\`

## 协议（与 Go 层一致）
- 设备类型：`earbud` / `glasses` / `ring`
- 上行（notify）：`WAKE` / `INTENT` / `SENSITIVE` / `BATTERY|{json}`
- 下行（write）：`APPROVE|{ticketId}` / `REJECT|{ticketId}`

## 安全边界（三条不可妥协之一：音频不出端）
BLE GATT 仅传输**结构化唤醒事件 / 意图分类结果 / 审批命令**。
原始音频在端侧处理（唤醒词 + 敏感过滤），永不上传。
`ble_device.dart` 的 `isAudioPayload` 恒为 false，Go 侧 `edge.AuditLog` 出端白名单对音频流量直接拦截。
