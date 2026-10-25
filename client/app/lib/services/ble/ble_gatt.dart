// v1.4.3: BLE GATT 协议常量——与服务端 internal/edge/ble.go 设备类型对齐。
// 原始音频永不出端；BLE 仅承载唤醒事件、意图分类结果与审批命令。
library;

/// IM2A BLE GATT Service UUID（128-bit，自定义）。
const String kIm2aServiceUuid = "0000im2a-0000-1000-8000-00805f9b34fb";

/// 特征：设备→手机（notify）。上报唤醒/意图/电量。
const String kCharNotifyUuid = "0000im2a-0001-1000-8000-00805f9b34fb";

/// 特征：手机→设备（write）。下发审批结果/指令。
const String kCharCommandUuid = "0000im2a-0002-1000-8000-00805f9b34fb";

/// 设备类型，与 Go edge.DeviceType 字符串一致。
enum BleDeviceType {
  earbud("earbud", "耳夹耳机"),
  glasses("glasses", "AI 眼镜"),
  ring("ring", "智能指环"),
  unknown("unknown", "未知设备");

  final String code;
  final String label;
  const BleDeviceType(this.code, this.label);

  static BleDeviceType fromCode(String? code) {
    return BleDeviceType.values.firstWhere(
      (t) => t.code == code,
      orElse: () => BleDeviceType.unknown,
    );
  }

  /// 从 BLE 广播名识别设备类型。
  static BleDeviceType fromDeviceName(String name) {
    final n = name.toLowerCase();
    if (n.contains("earbud") || n.contains("ear") || n.contains("耳")) {
      return BleDeviceType.earbud;
    }
    if (n.contains("glass") || n.contains("眼镜")) {
      return BleDeviceType.glasses;
    }
    if (n.contains("ring") || n.contains("指环")) {
      return BleDeviceType.ring;
    }
    return BleDeviceType.unknown;
  }
}

/// BLE 上行/下行命令字（与端侧 Pipeline 阶段对齐）。
abstract class BleCmd {
  static const String wake = "WAKE";          // 唤醒
  static const String intent = "INTENT";      // 意图分类结果
  static const String sensitive = "SENSITIVE";// 敏感词拦截
  static const String approve = "APPROVE";    // 审批通过
  static const String reject = "REJECT";      // 审批驳回
  static const String battery = "BATTERY";    // 电量
}
