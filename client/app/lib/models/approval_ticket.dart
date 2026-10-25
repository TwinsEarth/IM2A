/// 审批单模型（对应 proto/approval.proto 与 §7.1 审批态）。
library;

enum TicketState {
  pending,
  reviewing,
  approved,
  rejected,
  modified,
  executing,
  completed,
  failed,
}

extension TicketStateX on TicketState {
  String get label {
    switch (this) {
      case TicketState.pending:
        return '待审';
      case TicketState.reviewing:
        return '审阅中';
      case TicketState.approved:
        return '已通过';
      case TicketState.rejected:
        return '已驳回';
      case TicketState.modified:
        return '已修改';
      case TicketState.executing:
        return '执行中';
      case TicketState.completed:
        return '已完成';
      case TicketState.failed:
        return '失败';
    }
  }
}

/// 审批卡片：哪个子 Agent 发起 → 做了什么/发现了什么 → 建议动作 → 信任档位。
class ApprovalTicket {
  final String id;
  final String sourceAgentId;
  final String sourceAgentName;
  final String type; // NEGOTIATION / PAYMENT / SKILL_CALL / EXTERNAL_ACTION
  final TicketState state;
  final String summary; // 建议动作
  final String detail; // 做了什么 / 发现了什么
  final int amountCents; // 涉及金额（分）
  final String trustZone; // family / trusted / known / stranger
  final DateTime deadline;

  const ApprovalTicket({
    required this.id,
    required this.sourceAgentId,
    required this.sourceAgentName,
    required this.type,
    required this.state,
    required this.summary,
    required this.detail,
    required this.amountCents,
    required this.trustZone,
    required this.deadline,
  });

  String get amountLabel => amountCents == 0
      ? '无金额'
      : '¥${(amountCents / 100).toStringAsFixed(2)}';
}
