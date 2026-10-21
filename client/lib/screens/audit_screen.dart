// 模块职责：客户端「审计」页 —— 哈希链是否完整（可验证的"历史没被悄悄改写"）
import 'package:flutter/material.dart';

import '../api_client.dart';

class AuditScreen extends StatefulWidget {
  const AuditScreen({super.key, required this.client});

  final ApiClient client;

  @override
  State<AuditScreen> createState() => _AuditScreenState();
}

class _AuditScreenState extends State<AuditScreen> {
  Map<String, dynamic>? _data;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final data = await widget.client.audit();
      if (!mounted) return;
      setState(() {
        _data = data;
        _error = null;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = '加载失败：$error');
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) return Center(child: Text(_error!));
    final data = _data;
    if (data == null) return const Center(child: CircularProgressIndicator());

    final chain = (data['chain'] as Map?)?.cast<String, dynamic>() ?? const <String, dynamic>{};
    final entries = (data['entries'] as List? ?? const []).cast<Map<String, dynamic>>();
    final ok = chain['ok'] == true;
    return RefreshIndicator(
      onRefresh: _load,
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Card(
            color: ok ? Colors.green.shade50 : Colors.red.shade50,
            child: ListTile(
              leading: Icon(ok ? Icons.verified : Icons.report, color: ok ? Colors.green : Colors.red),
              title: Text(ok ? '审计链完整' : '审计链断裂'),
              subtitle: Text('${chain['length'] ?? 0} 条 · head ${(chain['head'] ?? '').toString().substring(0, 12)}…'),
            ),
          ),
          const SizedBox(height: 8),
          for (final entry in entries)
            ListTile(
              dense: true,
              leading: Text('#${entry['seq']}', style: const TextStyle(fontFamily: 'monospace', fontSize: 11)),
              title: Text('${entry['source']} · ${entry['action']}'),
              subtitle: Text('${entry['at']} · ${(entry['hash'] ?? '').toString().substring(0, 10)}…',
                  style: const TextStyle(fontFamily: 'monospace', fontSize: 11)),
            ),
        ],
      ),
    );
  }
}
