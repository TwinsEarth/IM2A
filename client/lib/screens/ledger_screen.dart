// 模块职责：客户端「账本」页 —— 付费消息的不可变账本（幂等键可见，便于对账）
import 'package:flutter/material.dart';

import '../api_client.dart';

class LedgerScreen extends StatefulWidget {
  const LedgerScreen({super.key, required this.client});

  final ApiClient client;

  @override
  State<LedgerScreen> createState() => _LedgerScreenState();
}

class _LedgerScreenState extends State<LedgerScreen> {
  Map<String, dynamic>? _data;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final data = await widget.client.ledger();
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

    final entries = (data['entries'] as List? ?? const []).cast<Map<String, dynamic>>();
    final stats = (data['stats'] as Map?)?.cast<String, dynamic>() ?? const <String, dynamic>{};
    return RefreshIndicator(
      onRefresh: _load,
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Text('条目 ${stats['count'] ?? 0} · 扣费 ${stats['charged'] ?? 0} · 退款 ${stats['refunded'] ?? 0} · 结算 ${stats['settled'] ?? 0}',
              style: const TextStyle(fontSize: 12, color: Colors.black54)),
          const SizedBox(height: 8),
          for (final entry in entries.reversed)
            ListTile(
              dense: true,
              leading: CircleAvatar(radius: 14, child: Text((entry['type'] ?? '').toString().substring(0, 1))),
              title: Text('${entry['type']} · ¥${((entry['amountCents'] as num? ?? 0) / 100).toStringAsFixed(2)}'),
              subtitle: Text('${entry['idempotencyKey']} · ${entry['at']}', style: const TextStyle(fontSize: 11)),
            ),
        ],
      ),
    );
  }
}
