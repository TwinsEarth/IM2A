/// 陌生人来意卡片（§7.2）：不直接进会话列表，先进过滤器队列。
library;

enum IntentState { pending, accepted, ignored, paid }

extension IntentStateX on IntentState {
  String get label {
    switch (this) {
      case IntentState.pending:
        return '待处理';
      case IntentState.accepted:
        return '已接受';
      case IntentState.ignored:
        return '已忽略';
      case IntentState.paid:
        return '已付费查看';
    }
  }
}

class StrangerIntent {
  final String intentId;
  final String fromAgentId;
  final String summary; // 对方 Agent 结构化来意：谁/为什么/期望什么
  final String negotiationSummary; // 双方 Agent 协商摘要
  final String billingMode; // send_charge / reply_charge
  final int amountCents; // 费用金额
  final IntentState state;

  const StrangerIntent({
    required this.intentId,
    required this.fromAgentId,
    required this.summary,
    required this.negotiationSummary,
    required this.billingMode,
    required this.amountCents,
    required this.state,
  });

  String get amountLabel => '¥${(amountCents / 100).toStringAsFixed(2)}';
  String get billingModeLabel =>
      billingMode == 'send_charge' ? '发送即扣费' : '回复才扣费';
}
