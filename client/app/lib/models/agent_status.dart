/// 子 Agent 工作状态（巡视态数据模型，§7.1）。
library;

/// 状态：working 工作中 / idle 待命 / blocked 阻塞 / needs_approval 待审批。
enum AgentWorkState { working, idle, blocked, needsApproval }

extension AgentWorkStateX on AgentWorkState {
  String get label {
    switch (this) {
      case AgentWorkState.working:
        return '工作中';
      case AgentWorkState.idle:
        return '待命';
      case AgentWorkState.blocked:
        return '阻塞';
      case AgentWorkState.needsApproval:
        return '待审批';
    }
  }
}

class AgentStatus {
  final String agentId;
  final String name;
  final String description;
  final AgentWorkState state;
  final String activity; // 当前动作描述
  final int pendingApprovals; // 待审批数
  final int todayActions; // 今日已完成动作数

  const AgentStatus({
    required this.agentId,
    required this.name,
    required this.description,
    required this.state,
    required this.activity,
    required this.pendingApprovals,
    required this.todayActions,
  });
}
