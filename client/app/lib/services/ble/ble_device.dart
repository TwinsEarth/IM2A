// v1.4.3: BLE 设备模型，对应 Go edge.Device。
library;
import "ble_gatt.dart";

class BleDevice {
  final String remoteId;       // 平台 MAC/UUID
  final String name;
  final BleDeviceType type;
  int battery;
  bool connected;
  int rssi;

  BleDevice({
    required this.remoteId,
    required this.name,
    required this.type,
    this.battery = 0,
    this.connected = false,
    this.rssi = 0,
  });

  /// 设备→手机上行事件。
  /// 仅允许唤醒/意图/电量等结构化数据，禁止原始音频（音频不出端原则）。
  bool get isAudioPayload => false;
}

/// 端侧上送的结构化事件（唤醒/意图/敏感拦截）。
class BleUplinkEvent {
  final String cmd;
  final Map<String, dynamic> payload;
  final DateTime at;
  BleUplinkEvent({required this.cmd, required this.payload, DateTime? at})
      : at = at ?? DateTime.now();

  factory BleUplinkEvent.parse(String raw) {
    // 格式：CMD|{json}
    final sep = raw.indexOf("|");
    if (sep < 0) {
      return BleUplinkEvent(cmd: raw.trim(), payload: {});
    }
    return BleUplinkEvent(
      cmd: raw.substring(0, sep).trim(),
      payload: {}, // 上层 jsonDecode 解析
    );
  }
}
