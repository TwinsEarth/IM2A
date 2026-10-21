import 'dart:async';

import 'package:flutter/material.dart';

import '../models/approval.dart';
import '../sdk/api_client.dart';
import '../sdk/state_store.dart';

/// 审批态（默认页）。
///
/// 设计约束（docs/07 §1.5）：一屏一卡、恰好三个动作、30 秒内完成一次决策；
/// 卡片只做展示，状态变更一律走服务端 `POST /api/approvals/{id}/decide`。
class ApprovalHome extends StatelessWidget {
  const ApprovalHome({super.key, required this.store});

  final StateStore store;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: store,
      builder: (BuildContext context, Widget? child) {
        final List<ApprovalTicket> pending = store.pendingApprovals;
        return Column(
          children: <Widget>[
            _StatusBanner(store: store),
            Expanded(
              child: RefreshIndicator(
                onRefresh: store.refresh,
                child: pending.isEmpty
                    ? _EmptyApprovalList(store: store)
                    : _buildList(context),
              ),
            ),
          ],
        );
      },
    );
  }

  Widget _buildList(BuildContext context) {
    final List<Widget> children = <Widget>[];
    store.pendingGroups.forEach((String label, List<ApprovalTicket> tickets) {
      children.add(
        Padding(
          padding: const EdgeInsets.fromLTRB(4, 12, 4, 6),
          child: Text(
            '$label · ${tickets.length}',
            style: Theme.of(context).textTheme.labelLarge,
          ),
        ),
      );
      for (final ApprovalTicket ticket in tickets) {
        children.add(ApprovalCard(ticket: ticket, store: store));
      }
    });
    return ListView(
      padding: const EdgeInsets.fromLTRB(12, 4, 12, 24),
      children: children,
    );
  }
}

class _StatusBanner extends StatelessWidget {
  const _StatusBanner({required this.store});

  final StateStore store;

  @override
  Widget build(BuildContext context) {
    final String? error = store.lastError;
    if (store.link == LinkState.online && error == null) {
      return const SizedBox.shrink();
    }
    final bool offline = store.link == LinkState.offline;
    return Container(
      width: double.infinity,
      color: offline ? Colors.red.shade50 : Colors.amber.shade100,
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
      child: Text(
        offline ? '离线：当前展示的是缓存状态，不能提交审批' : (error ?? '网络不稳定，正在重试'),
        style: TextStyle(
          fontSize: 12,
          color: offline ? Colors.red.shade900 : Colors.brown.shade900,
        ),
      ),
    );
  }
}

class _EmptyApprovalList extends StatelessWidget {
  const _EmptyApprovalList({required this.store});

  final StateStore store;

  @override
  Widget build(BuildContext context) {
    return ListView(
      children: <Widget>[
        const SizedBox(height: 96),
        const Icon(Icons.task_alt, size: 48),
        const SizedBox(height: 12),
        Center(
          child: Text(
            '今天没有需要你拍板的事',
            style: Theme.of(context).textTheme.titleMedium,
          ),
        ),
        const SizedBox(height: 6),
        Center(
          child: Text(
            '管家在替你盯着 · ${store.runningTickets.length} 项执行中',
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
      ],
    );
  }
}

/// 单张审批卡片（含 1s 倒计时刷新）。
class ApprovalCard extends StatefulWidget {
  const ApprovalCard({super.key, required this.ticket, required this.store});

  final ApprovalTicket ticket;
  final StateStore store;

  @override
  State<ApprovalCard> createState() => _ApprovalCardState();
}

class _ApprovalCardState extends State<ApprovalCard> {
  Timer? _ticker;

  @override
  void initState() {
    super.initState();
    _ticker = Timer.periodic(const Duration(seconds: 1), (Timer _) {
      if (mounted) setState(() {});
    });
  }

  @override
  void dispose() {
    _ticker?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final ApprovalTicket ticket = widget.ticket;
    final TextStyle? titleStyle = Theme.of(context).textTheme.titleMedium;
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 12, 14, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Row(
              children: <Widget>[
                Expanded(
                  child: Text(
                    '${ticket.agentLabel} · ${ticket.ticketType.label}',
                    style: Theme.of(context).textTheme.labelMedium,
                  ),
                ),
                _Tag(text: '${ticket.riskLevel.code} ${ticket.riskLevel.label}', color: _riskColor(ticket.riskLevel)),
                const SizedBox(width: 6),
                _Tag(text: ticket.trustZone.label, color: Colors.blueGrey),
              ],
            ),
            const SizedBox(height: 8),
            Text(ticket.digest, style: titleStyle),
            const SizedBox(height: 4),
            Text(
              '建议：${ticket.proposedAction}',
              style: Theme.of(context).textTheme.bodyMedium,
            ),
            if (ticket.hint != null) ...<Widget>[
              const SizedBox(height: 4),
              Text(
                ticket.hint ?? '',
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(fontSize: 12, color: Colors.black54),
              ),
            ],
            const SizedBox(height: 8),
            Row(
              children: <Widget>[
                if (ticket.amountCents != null)
                  Text(
                    ticket.amountLabel,
                    style: const TextStyle(fontWeight: FontWeight.bold),
                  ),
                const Spacer(),
                Text(
                  _countdownLabel(ticket),
                  style: TextStyle(
                    fontSize: 12,
                    color: _countdownColor(ticket),
                  ),
                ),
              ],
            ),
            const Divider(height: 18),
            Row(
              children: <Widget>[
                Expanded(
                  child: FilledButton(
                    onPressed: () => _approve(context, ticket),
                    child: Text(DecisionAction.approve.label),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: OutlinedButton(
                    onPressed: () => _modify(context, ticket),
                    child: Text(DecisionAction.modify.label),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: TextButton(
                    onPressed: () => _reject(context, ticket),
                    child: Text(DecisionAction.reject.label),
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  String _countdownLabel(ApprovalTicket ticket) {
    final Duration left = ticket.remainingAt(DateTime.now());
    if (left.isNegative) return '已超时';
    final String minutes = left.inMinutes.toString().padLeft(2, '0');
    final String seconds = (left.inSeconds % 60).toString().padLeft(2, '0');
    return '剩余 $minutes:$seconds';
  }

  Color _countdownColor(ApprovalTicket ticket) {
    final Duration left = ticket.remainingAt(DateTime.now());
    if (left.isNegative) return Colors.red;
    if (left.inMinutes < 1) return Colors.red;
    if (left.inMinutes < 5) return Colors.orange;
    return Colors.black54;
  }

  Color _riskColor(RiskLevel risk) {
    if (risk == RiskLevel.high) return Colors.red;
    if (risk == RiskLevel.medium) return Colors.orange;
    return Colors.green;
  }

  Future<void> _approve(BuildContext context, ApprovalTicket ticket) async {
    // L3 高风险：必须二次确认（客户端约束，见 docs/07 §1.2）。
    if (ticket.riskLevel == RiskLevel.high) {
      final String target = ticket.targetId ?? '—';
      final String dialogBody =
          '${ticket.digest}\n涉及：$target\n金额：${ticket.amountLabel}';
      final bool? confirmed = await showDialog<bool>(
        context: context,
        builder: (BuildContext dialogContext) => AlertDialog(
          title: const Text('高风险审批'),
          content: Text(dialogBody),
          actions: <Widget>[
            TextButton(
              onPressed: () => Navigator.of(dialogContext).pop(false),
              child: const Text('再想想'),
            ),
            FilledButton(
              onPressed: () => Navigator.of(dialogContext).pop(true),
              child: const Text('确认通过'),
            ),
          ],
        ),
      );
      if (confirmed != true) return;
    }
    await _run(
      context,
      () => widget.store.decide(
        ticketId: ticket.id,
        action: DecisionAction.approve,
      ),
    );
  }

  Future<void> _reject(BuildContext context, ApprovalTicket ticket) async {
    await _run(
      context,
      () => widget.store.decide(
        ticketId: ticket.id,
        action: DecisionAction.reject,
      ),
    );
  }

  Future<void> _modify(BuildContext context, ApprovalTicket ticket) async {
    final int? cents = await showModalBottomSheet<int>(
      context: context,
      isScrollControlled: true,
      builder: (BuildContext sheetContext) =>
          _ModifySheet(initialCents: ticket.amountCents),
    );
    if (cents == null) return;
    await _run(
      context,
      () => widget.store.decide(
        ticketId: ticket.id,
        action: DecisionAction.modify,
        patch: <String, dynamic>{'amountCents': cents},
      ),
    );
  }

  Future<void> _run(
    BuildContext context,
    Future<void> Function() action,
  ) async {
    try {
      await action();
    } on ApiException catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(humanizeError(error))),
      );
    }
  }
}

/// 「修改」只允许改结构化数值（金额），不允许自由文本改结论。
class _ModifySheet extends StatefulWidget {
  const _ModifySheet({required this.initialCents});

  final int? initialCents;

  @override
  State<_ModifySheet> createState() => _ModifySheetState();
}

class _ModifySheetState extends State<_ModifySheet> {
  late final TextEditingController _controller = TextEditingController(
    text: widget.initialCents == null
        ? ''
        : (widget.initialCents! / 100).toStringAsFixed(2),
  );
  String? _error;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  void _submit() {
    final double? yuan = double.tryParse(_controller.text.trim());
    if (yuan == null || yuan <= 0) {
      setState(() {
        _error = '请输入大于 0 的金额';
      });
      return;
    }
    Navigator.of(context).pop((yuan * 100).round());
  }

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsets.fromLTRB(
        16,
        20,
        16,
        20 + MediaQuery.of(context).viewInsets.bottom,
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Text('修改金额', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 4),
          const Text('只提交结构化 diff（amountCents），结论仍由你决定。'),
          const SizedBox(height: 12),
          TextField(
            controller: _controller,
            keyboardType: const TextInputType.numberWithOptions(decimal: true),
            decoration: InputDecoration(
              labelText: '金额（元）',
              errorText: _error,
              border: const OutlineInputBorder(),
            ),
          ),
          const SizedBox(height: 16),
          SizedBox(
            width: double.infinity,
            child: FilledButton(
              onPressed: _submit,
              child: const Text('提交修改'),
            ),
          ),
        ],
      ),
    );
  }
}

class _Tag extends StatelessWidget {
  const _Tag({required this.text, required this.color});

  final String text;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: color.withOpacity(0.12),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Text(text, style: TextStyle(fontSize: 11, color: color)),
    );
  }
}
