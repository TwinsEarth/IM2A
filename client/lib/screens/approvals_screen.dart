// 模块职责：客户端「审批」页 —— 人机混合里"人"的唯一落点：看卡片、做决策
import 'package:flutter/material.dart';

import '../api_client.dart';

class ApprovalsScreen extends StatefulWidget {
  const ApprovalsScreen({super.key, required this.client});

  final ApiClient client;

  @override
  State<ApprovalsScreen> createState() => _ApprovalsScreenState();
}

class _ApprovalsScreenState extends State<ApprovalsScreen> {
  Map<String, dynamic>? _state;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final data = await widget.client.adminState();
      if (!mounted) return;
      setState(() {
        _state = data;
        _error = null;
      });
    } on ApiException catch (error) {
      if (!mounted) return;
      setState(() => _error = error.toString());
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = '连接失败：$error');
    }
  }

  Future<void> _decide(String ticketId, String action) async {
    try {
      await widget.client.decide(ticketId, action);
      await _load();
    } on ApiException catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(error.toString())));
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            Text(_error!, textAlign: TextAlign.center),
            const SizedBox(height: 12),
            FilledButton(onPressed: _load, child: const Text('重试')),
          ]),
        ),
      );
    }
    final state = _state;
    if (state == null) return const Center(child: CircularProgressIndicator());

    final tickets = (state['tickets'] as List? ?? const []).cast<Map<String, dynamic>>();
    final messages = (state['messages'] as List? ?? const []).cast<Map<String, dynamic>>();
    return RefreshIndicator(
      onRefresh: _load,
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          for (final ticket in tickets) _TicketCard(ticket: ticket, onDecide: _decide),
          if (messages.isNotEmpty) ...[
            const SizedBox(height: 8),
            const Text('付费消息', style: TextStyle(fontWeight: FontWeight.w600)),
            for (final message in messages)
              ListTile(
                dense: true,
                title: Text((message['topic'] ?? '').toString()),
                subtitle: Text('${message['state']} · ¥${((message['amountCents'] as num? ?? 0) / 100).toStringAsFixed(2)}'),
              ),
          ],
        ],
      ),
    );
  }
}

class _TicketCard extends StatelessWidget {
  const _TicketCard({required this.ticket, required this.onDecide});

  final Map<String, dynamic> ticket;
  final void Function(String ticketId, String action) onDecide;

  @override
  Widget build(BuildContext context) {
    final payload = (ticket['payload'] as Map?)?.cast<String, dynamic>() ?? const <String, dynamic>{};
    final id = (ticket['id'] ?? '').toString();
    final state = (ticket['state'] ?? '').toString();
    final pending = state == 'PENDING';
    final amount = payload['amountCents'] as num?;
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            Expanded(child: Text((payload['topic'] ?? payload['item'] ?? ticket['ticketType'] ?? '').toString(),
                style: const TextStyle(fontWeight: FontWeight.w600))),
            Chip(label: Text(state), visualDensity: VisualDensity.compact),
          ]),
          if (amount != null) Text('金额 ¥${(amount / 100).toStringAsFixed(2)}'),
          Text('来源 ${ticket['sourceAgentId'] ?? ''}', style: const TextStyle(fontSize: 12, color: Colors.black54)),
          if (pending)
            Row(children: [
              FilledButton(onPressed: () => onDecide(id, 'APPROVE'), child: const Text('通过')),
              const SizedBox(width: 8),
              OutlinedButton(onPressed: () => onDecide(id, 'REJECT'), child: const Text('驳回')),
            ]),
        ]),
      ),
    );
  }
}
