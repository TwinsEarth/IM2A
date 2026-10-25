// v1.4.3: 可穿戴设备管理页——真实扫描/连接耳机/眼镜/指环。
import "package:flutter/material.dart";
import "../services/ble/ble_service.dart";
import "../services/ble/ble_gatt.dart";

class DevicesScreen extends StatefulWidget {
  final BleService ble;
  const DevicesScreen({super.key, required this.ble});
  @override
  State<DevicesScreen> createState() => _DevicesScreenState();
}

class _DevicesScreenState extends State<DevicesScreen> {
  List<BleDevice> _scanned = [];
  List<BleDevice> _connected = [];
  bool _scanning = false;

  IconData _iconFor(BleDeviceType t) => switch (t) {
    BleDeviceType.earbud => Icons.headset,
    BleDeviceType.glasses => Icons.visibility,
    BleDeviceType.ring => Icons.circle,
    BleDeviceType.unknown => Icons.bluetooth,
  };

  Future<void> _scan() async {
    setState(() => _scanning = true);
    try {
      await for (final list in widget.ble.scan(timeout: const Duration(seconds: 6))) {
        if (mounted) setState(() => _scanned = list);
      }
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("扫描失败：$e")));
    } finally {
      if (mounted) setState(() => _scanning = false);
    }
  }

  Future<void> _connect(BleDevice d) async {
    try {
      await widget.ble.connect(d);
      if (mounted) {
        setState(() => _connected = widget.ble.connectedDevices);
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("已连接 ${d.name}")));
      }
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("连接失败：$e")));
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text("可穿戴设备"), centerTitle: true,
        actions: [IconButton(onPressed: _scanning ? null : _scan,
          icon: Icon(_scanning ? Icons.hourglass_top : Icons.bluetooth_searching))],
      ),
      body: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          if (_connected.isNotEmpty) ...[
            const Text("已连接", style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            ..._connected.map((d) => Card(
              child: ListTile(
                leading: Icon(_iconFor(d.type), color: Colors.green),
                title: Text("${d.name}（${d.type.label}）"),
                subtitle: Text("电量 ${d.battery}% · 信号 ${d.rssi}dBm"),
                trailing: IconButton(icon: const Icon(Icons.link_off),
                  onPressed: () => widget.ble.disconnect(d.remoteId).then((_) =>
                    setState(() => _connected = widget.ble.connectedDevices))),
              ),
            )),
            const Divider(height: 32),
          ],
          const Text("附近设备", style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
          if (_scanned.isEmpty && !_scanning)
            const Padding(padding: EdgeInsets.all(24), child: Center(child: Text("点击右上角扫描附近设备"))),
          ..._scanned.map((d) => Card(
            child: ListTile(
              leading: Icon(_iconFor(d.type)),
              title: Text(d.name),
              subtitle: Text("${d.type.label} · ${d.rssi}dBm"),
              trailing: FilledButton(
                onPressed: () => _connect(d),
                child: const Text("连接"),
              ),
            ),
          )),
          const SizedBox(height: 16),
          const Card(
            color: Color(0xFFFFF8E1),
            child: Padding(padding: EdgeInsets.all(12),
              child: Row(children: [
                Icon(Icons.shield_outlined, color: Colors.orange),
                SizedBox(width: 8),
                Expanded(child: Text("隐私承诺：原始音频不离开设备，BLE 仅传输唤醒/意图/审批指令。",
                  style: TextStyle(fontSize: 12))),
              ])),
          ),
        ],
      ),
    );
  }
}
