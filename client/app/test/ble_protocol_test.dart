// v1.4.3: BLE 协议纯逻辑测试（不依赖真机蓝牙，验证协议解析与设备识别）。
import "package:flutter_test/flutter_test.dart";
import "package:im2a_client/services/ble/ble_gatt.dart";
import "package:im2a_client/services/ble/ble_device.dart";

void main() {
  group("BleDeviceType 设备识别", () {
    test("耳机", () => expect(BleDeviceType.fromDeviceName("IM2A Earbud Pro"), BleDeviceType.earbud));
    test("眼镜", () => expect(BleDeviceType.fromDeviceName("im2a-glasses"), BleDeviceType.glasses));
    test("指环", () => expect(BleDeviceType.fromDeviceName("Smart Ring"), BleDeviceType.ring));
    test("中文眼镜", () => expect(BleDeviceType.fromDeviceName("AI眼镜"), BleDeviceType.glasses));
    test("未知设备", () => expect(BleDeviceType.fromDeviceName("Foo"), BleDeviceType.unknown));
    test("fromCode 与 Go 对齐", () {
      expect(BleDeviceType.fromCode("earbud"), BleDeviceType.earbud);
      expect(BleDeviceType.fromCode("glasses"), BleDeviceType.glasses);
      expect(BleDeviceType.fromCode("ring"), BleDeviceType.ring);
      expect(BleDeviceType.fromCode("xxx"), BleDeviceType.unknown);
    });
  });

  group("BleUplinkEvent 解析", () {
    test("纯命令字", () {
      final e = BleUplinkEvent.parse("WAKE");
      expect(e.cmd, "WAKE");
      expect(e.payload, isEmpty);
    });
    test("带负载", () {
      final e = BleUplinkEvent.parse("BATTERY|{\"level\":87}");
      expect(e.cmd, "BATTERY");
      expect(e.payload, isEmpty); // JSON 由 BleService._parse 解析
    });
  });

  group("安全承诺", () {
    test("设备模型禁止音频载荷", () {
      final d = BleDevice(remoteId: "x", name: "IM2A Earbud", type: BleDeviceType.earbud);
      expect(d.isAudioPayload, isFalse);
    });
  });
}
