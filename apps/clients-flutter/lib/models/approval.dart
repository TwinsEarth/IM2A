/// 审批单模型。
///
/// 契约来源：`contracts/INTERFACES.md` §4（TICKET_STATE / TICKET_TYPE）。
/// 不变量：
///  1. 状态枚举的 wire 值必须与契约 `TICKET_STATE` 逐字一致，不得增删；
///  2. 金额一律整数分（cents），展示时才换算为 ¥；
///  3. 客户端不得自造状态（如 DONE / CANCELED / READ）。
library;

/// 审批单九态（契约 TICKET_STATE）。
enum TicketState {
  pending('PENDING'),
  reviewing('REVIEWING'),
  approved('APPROVED'),
  rejected('REJECTED'),
  modified('MODIFIED'),
  executing('EXECUTING'),
  completed('COMPLETED'),
  failed('FAILED'),
  expired('EXPIRED');

  const TicketState(this.wire);

  /// 与 `ticket.state` 字符串一致的线上值。
  final String wire;

  static TicketState parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final TicketState state in TicketState.values) {
      if (state.wire == value) return state;
    }
    throw ArgumentError.value(raw, 'state', '未知的 TICKET_STATE');
  }

  String get label {
    switch (this) {
      case TicketState.pending:
        return '待审批';
      case TicketState.reviewing:
        return '查看中';
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
        return '执行失败';
      case TicketState.expired:
        return '已超时';
    }
  }

  bool get isDecidable =>
      this == TicketState.pending || this == TicketState.reviewing;

  bool get isRunning => this == TicketState.executing;

  bool get isTerminal =>
      this == TicketState.completed ||
      this == TicketState.failed ||
      this == TicketState.rejected ||
      this == TicketState.expired;
}

/// 审批单类型（契约 TICKET_TYPE）。
enum TicketType {
  negotiation('NEGOTIATION', '协商'),
  payment('PAYMENT', '付款'),
  skillCall('SKILL_CALL', '能力调用'),
  externalAction('EXTERNAL_ACTION', '对外动作');

  const TicketType(this.wire, this.label);

  final String wire;
  final String label;

  static TicketType parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final TicketType type in TicketType.values) {
      if (type.wire == value) return type;
    }
    // 契约外的类型不崩溃，展示为「能力调用」并由审计发现。
    return TicketType.skillCall;
  }
}

/// 契约 `priority` 取值：urgent > high > normal > low。
enum TicketPriority {
  urgent('urgent', '紧急', 0),
  high('high', '高', 1),
  normal('normal', '普通', 2),
  low('low', '低', 3);

  const TicketPriority(this.wire, this.label, this.rank);

  final String wire;
  final String label;
  final int rank;

  static TicketPriority parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final TicketPriority priority in TicketPriority.values) {
      if (priority.wire == value) return priority;
    }
    return TicketPriority.normal;
  }
}

/// 审批动作（契约 `decide()` 的 action）。
enum DecisionAction {
  approve('APPROVE', '通过'),
  modify('MODIFY', '修改'),
  reject('REJECT', '驳回');

  const DecisionAction(this.wire, this.label);

  final String wire;
  final String label;
}

/// 五档信任权限（契约 TRUST_LEVELS / TRUST_NAMES）。
enum TrustZone {
  self(0, 'SELF', '本人'),
  family(1, 'FAMILY', '家庭'),
  trusted(2, 'TRUSTED', '信任'),
  known(3, 'KNOWN', '已知'),
  stranger(4, 'STRANGER', '陌生');

  const TrustZone(this.level, this.wire, this.label);

  final int level;
  final String wire;
  final String label;

  static TrustZone parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final TrustZone zone in TrustZone.values) {
      if (zone.wire == value) return zone;
    }
    // 缺省与过期授权一律按 STRANGER 处理（最保守）。
    return TrustZone.stranger;
  }
}

/// 卡片风险等级（由 payload.riskLevel 或门控 code 推导，见 docs/07 §1.2）。
enum RiskLevel {
  low('L1', '低'),
  medium('L2', '中'),
  high('L3', '高');

  const RiskLevel(this.code, this.label);

  final String code;
  final String label;

  static RiskLevel fromPayload(Map<String, dynamic> payload) {
    final Object? explicit = payload['riskLevel'] ?? payload['risk'];
    if (explicit is String) {
      switch (explicit.toUpperCase()) {
        case 'L3':
        case 'HIGH':
          return RiskLevel.high;
        case 'L2':
        case 'MEDIUM':
          return RiskLevel.medium;
        case 'L1':
        case 'LOW':
          return RiskLevel.low;
      }
    }
    final int? amount = ApprovalTicket.asCents(payload['amountCents']);
    final Object? gateCode = payload['gateCode'];
    if (gateCode == 'DESTRUCTIVE_ACTION') return RiskLevel.high;
    if (amount != null && amount >= 50000) return RiskLevel.high;
    if (gateCode == 'AMOUNT_THRESHOLD') return RiskLevel.medium;
    return RiskLevel.low;
  }
}

/// 审批单（契约 §4 `create()` 返回的 frozen ticket）。
class ApprovalTicket {
  const ApprovalTicket({
    required this.id,
    required this.userId,
    required this.sourceAgentId,
    required this.ticketType,
    required this.state,
    required this.payload,
    required this.createdAt,
    required this.deadline,
    required this.priority,
    this.traceId,
    this.patch,
  });

  final String id;
  final String userId;
  final String sourceAgentId;
  final TicketType ticketType;
  final TicketState state;
  final Map<String, dynamic> payload;
  final DateTime createdAt;
  final DateTime deadline;
  final TicketPriority priority;
  final String? traceId;
  final Map<String, dynamic>? patch;

  factory ApprovalTicket.fromJson(Map<String, dynamic> json) {
    return ApprovalTicket(
      id: _text(json['id']) ?? '',
      userId: _text(json['userId']) ?? '',
      sourceAgentId: _text(json['sourceAgentId']) ?? '',
      ticketType: TicketType.parse(json['ticketType']),
      state: TicketState.parse(json['state']),
      payload: _map(json['payload']),
      createdAt: _date(json['createdAt']),
      deadline: _date(json['deadline']),
      priority: TicketPriority.parse(json['priority']),
      traceId: _text(json['traceId']),
      patch: json['patch'] is Map ? _map(json['patch']) : null,
    );
  }

  /// 卡片第 2 行：一句话结论（服务端 payload 优先，其次按演示数据兜底）。
  String get digest {
    final String? explicit = _text(payload['digest']);
    if (explicit != null && explicit.isNotEmpty) return explicit;
    return _text(payload['item']) ??
        _text(payload['topic']) ??
        _text(payload['title']) ??
        '需要你决策';
  }

  /// 卡片第 3 行：建议动作。
  String get proposedAction {
    final String? explicit = _text(payload['proposedAction']);
    if (explicit != null && explicit.isNotEmpty) return explicit;
    return _text(payload['vendor']) ??
        _text(payload['item']) ??
        _text(payload['skillId']) ??
        '待确认';
  }

  /// 服务端给出的人类可读解释（卡片折叠区）。
  String? get hint => _text(payload['decisionHint']);

  /// 金额（分）。契约里金额字段以 Cents 结尾。
  int? get amountCents => asCents(payload['amountCents']);

  String? get targetId => _text(payload['targetId']);

  TrustZone get trustZone => TrustZone.parse(payload['trustZone']);

  RiskLevel get riskLevel => RiskLevel.fromPayload(payload);

  String get agentLabel => agentDisplayName(sourceAgentId);

  String get amountLabel =>
      amountCents == null ? '—' : formatYuan(amountCents ?? 0);

  Duration remainingAt(DateTime now) => deadline.difference(now);

  bool isOverdueAt(DateTime now) => !deadline.isAfter(now);

  /// 修改动作回传的结构化 diff（客户端不本地改状态）。
  ApprovalTicket copyWith({TicketState? state, Map<String, dynamic>? patch}) {
    return ApprovalTicket(
      id: id,
      userId: userId,
      sourceAgentId: sourceAgentId,
      ticketType: ticketType,
      state: state ?? this.state,
      payload: payload,
      createdAt: createdAt,
      deadline: deadline,
      priority: priority,
      traceId: traceId,
      patch: patch ?? this.patch,
    );
  }

  static int? asCents(Object? raw) {
    if (raw is int) return raw;
    if (raw is num) return raw.toInt();
    if (raw is String) return int.tryParse(raw);
    return null;
  }
}

/// 分 -> ¥ 字符串（只在展示层换算）。
String formatYuan(int cents) => '¥${(cents / 100).toStringAsFixed(2)}';

/// 子 Agent 展示名（契约的 agentId 前缀约定：agent-*）。
///
/// 先匹配演示世界的具体 Agent，再用域名关键词兜底，最后退化为去前缀的裸 id。
String agentDisplayName(String agentId) {
  const Map<String, String> known = <String, String>{
    'agent-mail': '工作邮件',
    'agent-shopping': '采购比价',
    'agent-utility': '物业水电煤',
    'agent-calendar': '日程预约',
    'agent-legal': '法律',
    'agent-repair': '家政维修',
    'agent-edu': '教育亲子',
    'agent-callfilter': '电话过滤',
    'agent-butler': '管家',
    // 演示世界（apps/demo-server/flow.mjs）
    'agent-lin-butler': '管家（林总）',
    'agent-lin-stranger-filter': '陌生人过滤',
    'agent-lin-procurement': '采购比价',
    'agent-lin-family': '家庭事务',
    'agent-zhou-butler': '对方管家',
  };
  final String? hit = known[agentId];
  if (hit != null) return hit;
  if (agentId.contains('stranger-filter')) return '陌生人过滤';
  if (agentId.contains('procurement')) return '采购比价';
  if (agentId.contains('family')) return '家庭事务';
  if (agentId.endsWith('-butler')) return '管家';
  final String cleaned =
      agentId.replaceFirst(RegExp(r'^agent-'), '').replaceAll('-', ' ');
  return cleaned.isEmpty ? agentId : cleaned;
}

String? _text(Object? value) {
  if (value == null) return null;
  final String text = '$value';
  return text.isEmpty ? null : text;
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
