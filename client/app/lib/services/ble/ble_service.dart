// v1.4.3: 真实 BLE 网关——基于 flutter_blue_plus。
// 扫描/连接耳机/眼镜/指环，订阅 notify 接收结构化事件，write 下发审批命令。
// 安全承诺：本通道不传输任何原始音频，仅结构化指令（对应 Go edge.AuditLog 白名单）。
library;
import "dart:async";
import "dart:convert";
import "package:flutter_blue_plus/flutter_blue_plus.dart";
import "ble_gatt.dart";
import "ble_device.dart";

class BleService {
  // 已连接设备：remoteId -> BleDevice
  final Map<String, BleDevice> _connected = {};
  final Map<String, BluetoothDevice> _raw = {};
  final Map<String, StreamSubscription> _subs = {};
  final _eventCtrl = StreamController<BleUplinkEvent>.broadcast();
  final _deviceCtrl = StreamController<List<BleDevice>>.broadcast();

  /// 设备→手机上行事件流（唤醒/意图/敏感拦截/电量）。
  Stream<BleUplinkEvent> get events => _eventCtrl.stream;
  /// 已连接设备列表流。
  Stream<List<BleDevice>> get devices => _deviceCtrl.stream;
  List<BleDevice> get connectedDevices => _connected.values.toList();

  /// 蓝牙是否可用。
  Future<bool> get isAvailable => FlutterBluePlus.isAvailable;

  /// 扫描附近 IM2A 设备，超时返回扫描结果（不自动连接）。
  Stream<List<BleDevice>> scan({Duration timeout = const Duration(seconds: 8)}) async* {
    final found = <String, BleDevice>{};
    var sub = FlutterBluePlus.scanResults.listen((results) {
      for (final r in results) {
        final name = r.device.platformName.isEmpty ? r.advertisementData.advName : r.device.platformName;
        if (name.isEmpty) continue;
        final type = BleDeviceType.fromDeviceName(name);
        // 只收 IM2A 类型设备或广播名带 IM2A 的设备
        final isIm2a = name.toUpperCase().contains("IM2A") || type != BleDeviceType.unknown;
        if (!isIm2a) continue;
        found[r.device.remoteId.str] = BleDevice(
          remoteId: r.device.remoteId.str,
          name: name,
          type: type,
          rssi: r.rssi,
        );
        _deviceCtrl.add(found.values.toList());
      }
    });
    await FlutterBluePlus.startScan(timeout: timeout);
    await Future.delayed(timeout);
    await FlutterBluePlus.stopScan();
    await sub.cancel();
    yield found.values.toList();
  }

  /// 连接设备并发现 IM2A GATT 服务、订阅 notify。
  Future<BleDevice> connect(BleDevice target) async {
    final device = BluetoothDevice.fromId(target.remoteId);
    await device.connect(timeout: const Duration(seconds: 10));
    _raw[target.remoteId] = device;

    // 发现服务
    final services = await device.discoverServices();
    BluetoothCharacteristic? notifyCh;
    BluetoothCharacteristic? cmdCh;
    for (final svc in services) {
      if (svc.uuid.str.toLowerCase() == kIm2aServiceUuid.toLowerCase()) {
        for (final ch in svc.characteristics) {
          if (ch.uuid.str.toLowerCase() == kCharNotifyUuid.toLowerCase()) notifyCh = ch;
          if (ch.uuid.str.toLowerCase() == kCharCommandUuid.toLowerCase()) cmdCh = ch;
        }
      }
    }

    // 订阅设备→手机
    if (notifyCh != null) {
      await notifyCh.setNotifyValue(true);
      final sub = notifyCh.lastValueStream.listen((value) {
        if (value.isEmpty) return;
        final raw = utf8.decode(value, allowMalformed: true);
        final ev = _parse(raw);
        if (ev != null) {
          if (ev.cmd == BleCmd.battery) {
            target.battery = int.tryParse("${ev.payload["level"]}") ?? target.battery;
          }
          _eventCtrl.add(ev);
        }
      });
      _subs[target.remoteId] = sub;
    }

    // 缓存命令特征供 write
    _cmdChars[target.remoteId] = cmdCh;

    target.connected = true;
    _connected[target.remoteId] = target;
    _deviceCtrl.add(_connected.values.toList());
    device.cancelWhenDisconnected(_onDisconnect(target.remoteId));
    return target;
  }

  final Map<String, BluetoothCharacteristic?> _cmdChars = {};

  /// 手机→设备：下发审批命令。
  Future<bool> sendCommand(String remoteId, String cmd, [Map<String, dynamic>? payload]) async {
    final ch = _cmdChars[remoteId];
    if (ch == null) return false;
    final body = payload == null ? cmd : "$cmd|${jsonEncode(payload)}";
    await ch.write(utf8.encode(body));
    return true;
  }

  /// 便捷：审批通过/驳回下发到可穿戴设备。
  Future<bool> approve(String remoteId, String ticketId) =>
      sendCommand(remoteId, BleCmd.approve, {"ticketId": ticketId});
  Future<bool> reject(String remoteId, String ticketId) =>
      sendCommand(remoteId, BleCmd.reject, {"ticketId": ticketId});

  Future<void> disconnect(String remoteId) async {
    await _subs.remove(remoteId)?.cancel();
    await _raw[remoteId]?.disconnect();
    _onDisconnect(remoteId)();
  }

  void _onDisconnect(String remoteId) {
    _connected.remove(remoteId);
    _raw.remove(remoteId);
    _cmdChars.remove(remoteId);
    _deviceCtrl.add(_connected.values.toList());
  }

  BleUplinkEvent? _parse(String raw) {
    final sep = raw.indexOf("|");
    final cmd = (sep < 0 ? raw : raw.substring(0, sep)).trim();
    Map<String, dynamic> payload = {};
    if (sep >= 0) {
      try {
        payload = jsonDecode(raw.substring(sep + 1));
      } catch (_) {}
    }
    return BleUplinkEvent(cmd: cmd, payload: payload);
  }

  Future<void> dispose() async {
    for (final id in _subs.keys.toList()) {
      await disconnect(id);
    }
    await _eventCtrl.close();
    await _deviceCtrl.close();
  }
}
