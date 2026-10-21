/// 陌生人付费消息模型。
///
/// 契约来源：`contracts/INTERFACES.md` §9（MSG_STATE / BILLING）。
/// 铁律：**先 Agent 预审（screen），后付费（pay）**；付费永不能绕过过滤。
library;

import 'approval.dart' show formatYuan;

/// 付费消息九态（契约 MSG_STATE）。
enum MsgState {
  intent('INTENT'),
  awaitingPayment('AWAITING_PAYMENT'),
  refused('REFUSED'),
  paid('PAID'),
  viewed('VIEWED'),
  replied('REPLIED'),
  settled('SETTLED'),
  refunded('REFUNDED'),
  expired('EXPIRED');

  const MsgState(this.wire);

  final String wire;

  static MsgState parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final MsgState state in MsgState.values) {
      if (state.wire == value) return state;
    }
    throw ArgumentError.value(raw, 'state', '未知的 MSG_STATE');
  }

  String get label {
    switch (this) {
      case MsgState.intent:
        return '对方正在说明来意';
      case MsgState.awaitingPayment:
        return '预审通过，待你决定';
      case MsgState.refused:
        return '未通过预审';
      case MsgState.paid:
        return '已付费，待查看';
      case MsgState.viewed:
        return '已查看';
      case MsgState.replied:
        return '已回复';
      case MsgState.settled:
        return '已结算';
      case MsgState.refunded:
        return '已退款';
      case MsgState.expired:
        return '已过期';
    }
  }

  /// 只有预审通过（AWAITING_PAYMENT）才可能出现付费动作。
  bool get canPay => this == MsgState.awaitingPayment;

  bool get canView => this == MsgState.paid;

  bool get canReply => this == MsgState.viewed;
}

/// 两档计费（契约 BILLING：SEND_PAY=1 发送即扣费，VIEW_REPLY_PAY=2 看到且回复才扣费）。
enum BillingMode {
  sendPay(1, '发送即扣费', '用于过滤批量垃圾信息'),
  viewReplyPay(2, '看到且回复才扣费', '用于高价值人脉');

  const BillingMode(this.code, this.label, this.note);

  final int code;
  final String label;
  final String note;

  static BillingMode parse(Object? raw) {
    if (raw is int) {
      for (final BillingMode mode in BillingMode.values) {
        if (mode.code == raw) return mode;
      }
    }
    final String value = raw is String ? raw : '';
    for (final BillingMode mode in BillingMode.values) {
      if (mode.wire == value) return mode;
    }
    return BillingMode.sendPay;
  }

  String get wire => this == BillingMode.sendPay ? 'SEND_PAY' : 'VIEW_REPLY_PAY';
}

/// 陌生人来意的四要素（docs/07 §3.1）：
/// 谁 / 为什么 / 期望什么 / 多紧急。
class StrangerIntent {
  const StrangerIntent({
    required this.who,
    required this.why,
    required this.expect,
    required this.urgency,
  });

  final String who;
  final String why;
  final String expect;
  final String urgency;

  factory StrangerIntent.fromJson(Map<String, dynamic> json) {
    return StrangerIntent(
      who: _text(json['who']) ?? '未说明身份',
      why: _text(json['why']) ?? '未说明来意',
      expect: _text(json['expect']) ?? '未说明期望',
      urgency: _text(json['urgency']) ?? 'normal',
    );
  }

  String get urgencyLabel {
    switch (urgency) {
      case 'urgent':
        return '紧急';
      case 'high':
        return '较高';
      case 'low':
        return '不急';
      default:
        return '普通';
    }
  }
}

/// 付费消息（契约 §9 `submitIntent()` 与其后状态机）。
class PaidMessage {
  const PaidMessage({
    required this.id,
    required this.senderUserId,
    required this.senderAgentId,
    required this.receiverUserId,
    required this.topic,
    required this.summary,
    required this.state,
    required this.billingMode,
    required this.amountCents,
    required this.createdAt,
    required this.intent,
    this.negotiationRounds,
    this.screened,
  });

  final String id;
  final String senderUserId;
  final String senderAgentId;
  final String receiverUserId;
  final String topic;
  final String summary;
  final MsgState state;
  final BillingMode billingMode;
  final int amountCents;
  final DateTime createdAt;

  /// 结构化来意（来意卡片四要素）。
  final StrangerIntent intent;

  /// 双方 Agent 协商轮次（摘要展示用，可为空）。
  final int? negotiationRounds;

  /// 目标用户 Agent 的预审结论：PASS / BLOCK / null（未审）。
  final String? screened;

  factory PaidMessage.fromJson(Map<String, dynamic> json) {
    final Map<String, dynamic> payload = _map(json['payload']);
    final Object? rawIntent = payload['intent'] ?? json['intent'];
    return PaidMessage(
      id: _text(json['id']) ?? '',
      senderUserId: _text(json['senderUserId']) ?? '',
      senderAgentId: _text(json['senderAgentId']) ?? '',
      receiverUserId: _text(json['receiverUserId']) ?? '',
      topic: _text(json['topic']) ?? '陌生人来意',
      summary: _text(json['summary']) ?? '',
      state: MsgState.parse(json['state']),
      billingMode: BillingMode.parse(json['billingMode']),
      amountCents: _int(json['amountCents']) ?? 0,
      createdAt: _date(json['createdAt']),
      intent: StrangerIntent.fromJson(
        rawIntent is Map ? _map(rawIntent) : const <String, dynamic>{},
      ),
      negotiationRounds: _int(payload['negotiationRounds']),
      screened: _text(json['screened'] ?? payload['screened']),
    );
  }

  String get amountLabel => formatYuan(amountCents);

  bool get screenedPass => screened == 'PASS';

  bool get screenedBlock => screened == 'BLOCK';
}

String? _text(Object? value) => value == null ? null : '$value';

int? _int(Object? value) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  if (value is String) return int.tryParse(value);
  return null;
}

DateTime _date(Object? value) {
  if (value is String) {
    final DateTime? parsed = DateTime.tryParse(value);
    if (parsed != null) return parsed.toLocal();
  }
  return DateTime.fromMillisecondsSinceEpoch(0);
}

Map<String, dynamic> _map(Object? value) {
  if (value is Map) {
    return value.map((Object? key, Object? val) =>
        MapEntry<String, dynamic>('$key', val));
  }
  return const <String, dynamic>{};
}
