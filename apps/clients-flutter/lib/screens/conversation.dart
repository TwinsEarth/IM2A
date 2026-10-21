import 'package:flutter/material.dart';

import '../models/approval.dart';
import '../models/paid_message.dart';
import '../sdk/api_client.dart';
import '../sdk/state_store.dart';

/// 陌生人「来意卡片」与会话页。
///
/// 铁律（契约 §9）：付费发生在 `screen()` 预审之后；
/// 用户看到的是「忽略 / 接受并查看」两个动作——把注意力准入与付费合并成一次判断。
class ConversationScreen extends StatelessWidget {
  const ConversationScreen({
    super.key,
    required this.store,
    required this.messageId,
  });

  final StateStore store;
  final String messageId;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: store,
      builder: (BuildContext context, Widget? child) {
        final PaidMessage? message = store.messageById(messageId);
        if (message == null) {
          return Scaffold(
            appBar: AppBar(title: const Text('来意')),
            body: const Center(child: Text('该消息已不存在或已过期')),
          );
        }
        // 来意四要素 / 预审结论 / 协商轮次挂在关联审批单的 payload 上。
        final ApprovalTicket? linked = store.ticketForMessage(message.id);
        return Scaffold(
          appBar: AppBar(title: Text(message.topic)),
          body: ListView(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 32),
            children: <Widget>[
              _intentCard(context, message, linked),
              const SizedBox(height: 12),
              _screeningCard(context, message, linked),
              const SizedBox(height: 12),
              _billingCard(context, message, linked),
              const SizedBox(height: 12),
              _timelineCard(context, message),
              const SizedBox(height: 20),
              ..._actions(context, message),
            ],
          ),
        );
      },
    );
  }

  Widget _sectionTitle(BuildContext context, String text) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Text(text, style: Theme.of(context).textTheme.labelLarge),
    );
  }

  Widget _row(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          SizedBox(
            width: 64,
            child: Text(label, style: const TextStyle(color: Colors.black54)),
          ),
          Expanded(child: Text(value)),
        ],
      ),
    );
  }

  Widget _intentCard(
    BuildContext context,
    PaidMessage message,
    ApprovalTicket? linked,
  ) {
    final String who =
        _text(linked?.payload['senderName']) ?? message.intent.who;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Row(
              children: <Widget>[
                Expanded(child: _sectionTitle(context, '来意（双方 Agent 协商后）')),
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                  decoration: BoxDecoration(
                    color: Colors.orange.withOpacity(0.12),
                    borderRadius: BorderRadius.circular(10),
                  ),
                  child: Text(
                    message.intent.urgencyLabel,
                    style: const TextStyle(fontSize: 11, color: Colors.orange),
                  ),
                ),
              ],
            ),
            _row('谁', who),
            _row('为什么', message.intent.why),
            _row('期望', message.intent.expect),
            _row('紧急', message.intent.urgencyLabel),
            if (message.summary.isNotEmpty) ...<Widget>[
              const Divider(height: 18),
              Text(message.summary),
            ],
          ],
        ),
      ),
    );
  }

  Widget _screeningCard(
    BuildContext context,
    PaidMessage message,
    ApprovalTicket? linked,
  ) {
    final Map<String, dynamic> screening =
        _mapOf(linked?.payload['screening']);
    final String? code = _text(screening['verdict']) ??
        (message.screenedBlock
            ? 'BLOCK'
            : message.screenedPass
                ? 'PASS'
                : null);
    final String verdict = code == 'BLOCK'
        ? '未通过（BLOCK）'
        : code == 'PASS'
            ? '通过（PASS）'
            : '待你的 Agent 预审';
    final String? rule = _text(screening['rule']);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            _sectionTitle(context, '预审结论'),
            Text('目标用户 Agent 预审：$verdict'),
            if (rule != null) Text('命中规则：$rule'),
            const SizedBox(height: 6),
            const Text(
              '铁律：付费只能买「通道」，不能买「绕过过滤」。'
              '未通过预审的消息不会出现在这里，也不能付费。',
              style: TextStyle(fontSize: 12, color: Colors.black54),
            ),
          ],
        ),
      ),
    );
  }

  Widget _billingCard(
    BuildContext context,
    PaidMessage message,
    ApprovalTicket? linked,
  ) {
    final Object? rawRounds =
        _mapOf(linked?.payload['negotiation'])['rounds'] ??
            message.negotiationRounds;
    final String? rounds = _text(rawRounds);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            _sectionTitle(context, '计费'),
            Row(
              children: <Widget>[
                Expanded(child: Text(message.billingMode.label)),
                Text(
                  message.amountLabel,
                  style: const TextStyle(
                    fontWeight: FontWeight.bold,
                    fontSize: 16,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 4),
            Text(
              message.billingMode.note,
              style: const TextStyle(fontSize: 12, color: Colors.black54),
            ),
            if (rounds != null)
              Text(
                '双方 Agent 协商 $rounds 轮后达成来意摘要',
                style: const TextStyle(fontSize: 12, color: Colors.black54),
              ),
          ],
        ),
      ),
    );
  }

  Widget _timelineCard(BuildContext context, PaidMessage message) {
    const List<MsgState> happyPath = <MsgState>[
      MsgState.intent,
      MsgState.awaitingPayment,
      MsgState.paid,
      MsgState.viewed,
      MsgState.replied,
      MsgState.settled,
    ];
    final int current = happyPath.indexOf(message.state);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            _sectionTitle(context, '状态（MSG_STATE）'),
            Wrap(
              spacing: 6,
              runSpacing: 6,
              children: happyPath.map((MsgState state) {
                final bool active = state == message.state;
                final bool passed = current >= 0 && happyPath.indexOf(state) < current;
                return Chip(
                  label: Text(state.wire, style: const TextStyle(fontSize: 10)),
                  backgroundColor: active
                      ? Colors.blue.shade100
                      : passed
                          ? Colors.green.shade50
                          : null,
                );
              }).toList(growable: false),
            ),
            if (current < 0) ...<Widget>[
              const SizedBox(height: 6),
              Text(
                '终态：${message.state.wire}（${message.state.label}）',
                style: const TextStyle(fontSize: 12),
              ),
            ],
          ],
        ),
      ),
    );
  }

  List<Widget> _actions(BuildContext context, PaidMessage message) {
    final bool online = store.link == LinkState.online;
    if (message.state.canPay) {
      return <Widget>[
        Row(
          children: <Widget>[
            Expanded(
              child: OutlinedButton(
                onPressed: () => Navigator.of(context).maybePop(),
                child: const Text('忽略'),
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: FilledButton(
                onPressed: online
                    ? () => _acceptAndView(context, message)
                    : null,
                child: const Text('接受并查看'),
              ),
            ),
          ],
        ),
        const SizedBox(height: 8),
        const Text(
          '「忽略」不扣费；「接受并查看」= 一次性完成付费与打开。',
          style: TextStyle(fontSize: 12, color: Colors.black54),
        ),
      ];
    }
    if (message.state.canView) {
      return <Widget>[
        FilledButton(
          onPressed: online ? () => _view(context, message) : null,
          child: const Text('打开查看'),
        ),
      ];
    }
    if (message.state.canReply) {
      return <Widget>[
        _ReplyBox(
          enabled: online,
          onSubmit: (String text) => _reply(context, message, text),
        ),
      ];
    }
    return <Widget>[
      Text(
        '当前状态：${message.state.wire}（${message.state.label}），无可用动作。',
        style: const TextStyle(fontSize: 12, color: Colors.black54),
      ),
    ];
  }

  Future<void> _acceptAndView(BuildContext context, PaidMessage message) async {
    try {
      await store.payMessage(message.id);
      await store.viewMessage(message.id);
    } on ApiException catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(humanizeError(error))),
      );
    }
  }

  Future<void> _view(BuildContext context, PaidMessage message) async {
    try {
      await store.viewMessage(message.id);
    } on ApiException catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(humanizeError(error))),
      );
    }
  }

  Future<void> _reply(
    BuildContext context,
    PaidMessage message,
    String text,
  ) async {
    try {
      await store.replyMessage(message.id, text);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('已回复，等待结算')),
      );
    } on ApiException catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(humanizeError(error))),
      );
    }
  }
}

class _ReplyBox extends StatefulWidget {
  const _ReplyBox({required this.enabled, required this.onSubmit});

  final bool enabled;
  final Future<void> Function(String text) onSubmit;

  @override
  State<_ReplyBox> createState() => _ReplyBoxState();
}

class _ReplyBoxState extends State<_ReplyBox> {
  final TextEditingController _controller = TextEditingController();
  bool _sending = false;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    final String text = _controller.text.trim();
    if (text.isEmpty || _sending) return;
    setState(() {
      _sending = true;
    });
    await widget.onSubmit(text);
    if (!mounted) return;
    setState(() {
      _sending = false;
    });
    _controller.clear();
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: <Widget>[
        TextField(
          controller: _controller,
          enabled: widget.enabled && !_sending,
          maxLines: 3,
          decoration: const InputDecoration(
            labelText: '回复（人类回复，不经 Agent 代笔）',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 8),
        SizedBox(
          width: double.infinity,
          child: FilledButton(
            onPressed: widget.enabled && !_sending ? _submit : null,
            child: Text(_sending ? '发送中…' : '发送回复'),
          ),
        ),
      ],
    );
  }
}

String? _text(Object? value) {
  if (value == null) return null;
  final String text = '$value';
  return text.isEmpty ? null : text;
}

Map<String, dynamic> _mapOf(Object? value) {
  if (value is Map) {
    return value.map((Object? key, Object? val) =>
        MapEntry<String, dynamic>('$key', val));
  }
  return const <String, dynamic>{};
}
