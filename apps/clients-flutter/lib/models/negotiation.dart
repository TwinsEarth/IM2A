/// Agent↔Agent 结构化协商模型。
///
/// 契约来源：`contracts/INTERFACES.md` §5（TASK_STATE / INTENT）。
/// 不变量：协商中间过程不产生用户可见通知；客户端只展示终止结果（artifact）与摘要。
library;

import 'approval.dart' show agentDisplayName;

/// 协商任务八态（契约 TASK_STATE）。
enum TaskState {
  submitted('SUBMITTED'),
  working('WORKING'),
  inputRequired('INPUT_REQUIRED'),
  authRequired('AUTH_REQUIRED'),
  completed('COMPLETED'),
  failed('FAILED'),
  canceled('CANCELED'),
  rejected('REJECTED');

  const TaskState(this.wire);

  final String wire;

  static TaskState parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final TaskState state in TaskState.values) {
      if (state.wire == value) return state;
    }
    throw ArgumentError.value(raw, 'state', '未知的 TASK_STATE');
  }

  String get label {
    switch (this) {
      case TaskState.submitted:
        return '已提交';
      case TaskState.working:
        return '协商中';
      case TaskState.inputRequired:
        return '需要你补充信息';
      case TaskState.authRequired:
        return '需要你授权';
      case TaskState.completed:
        return '已达成';
      case TaskState.failed:
        return '失败';
      case TaskState.canceled:
        return '已取消';
      case TaskState.rejected:
        return '已拒绝';
    }
  }

  bool get isTerminal =>
      this == TaskState.completed ||
      this == TaskState.failed ||
      this == TaskState.canceled ||
      this == TaskState.rejected;

  /// 需要人介入的中断点（人机混合的协议级断点）。
  bool get needsHuman =>
      this == TaskState.inputRequired || this == TaskState.authRequired;
}

/// 协商意图（契约 INTENT）。
enum NegotiationIntent {
  propose('PROPOSE', '提议'),
  counter('COUNTER', '还价'),
  accept('ACCEPT', '接受'),
  reject('REJECT', '拒绝'),
  requestInfo('REQUEST_INFO', '请求信息'),
  requestAuth('REQUEST_AUTH', '请求授权');

  const NegotiationIntent(this.wire, this.label);

  final String wire;
  final String label;

  static NegotiationIntent parse(Object? raw) {
    final String value = raw is String ? raw : '';
    for (final NegotiationIntent intent in NegotiationIntent.values) {
      if (intent.wire == value) return intent;
    }
    return NegotiationIntent.requestInfo;
  }
}

/// 一轮协商发言。
class NegotiationTurn {
  const NegotiationTurn({
    required this.fromAgentId,
    required this.intent,
    required this.payload,
    required this.tokens,
    required this.at,
  });

  final String fromAgentId;
  final NegotiationIntent intent;
  final Map<String, dynamic> payload;
  final int tokens;
  final DateTime at;

  factory NegotiationTurn.fromJson(Map<String, dynamic> json) {
    return NegotiationTurn(
      fromAgentId: _text(json['fromAgentId']) ?? '',
      intent: NegotiationIntent.parse(json['intent']),
      payload: _map(json['payload']),
      tokens: _int(json['tokens']) ?? 0,
      at: _date(json['at']),
    );
  }

  String get summary => _text(payload['summary']) ?? intent.label;
}

/// 终止产物（契约 `artifact()`：type 目前只有 AGREEMENT）。
class NegotiationArtifact {
  const NegotiationArtifact({
    required this.type,
    required this.summary,
    required this.at,
  });

  final String type;
  final String summary;
  final DateTime at;

  factory NegotiationArtifact.fromJson(Map<String, dynamic> json) {
    return NegotiationArtifact(
      type: _text(json['type']) ?? 'AGREEMENT',
      summary: _text(json['summary']) ?? '',
      at: _date(json['at']),
    );
  }
}

/// 协商会话（契约 §5 `open()` 返回的 frozen session）。
class NegotiationSession {
  const NegotiationSession({
    required this.id,
    required this.topic,
    required this.state,
    required this.round,
    required this.tokens,
    required this.initiatorAgentId,
    required this.initiatorUserId,
    required this.responderAgentId,
    required this.responderUserId,
    required this.turns,
    this.terminalReason,
    this.artifact,
  });

  final String id;
  final String topic;
  final TaskState state;
  final int round;
  final int tokens;
  final String initiatorAgentId;
  final String initiatorUserId;
  final String responderAgentId;
  final String responderUserId;
  final List<NegotiationTurn> turns;
  final String? terminalReason;
  final NegotiationArtifact? artifact;

  factory NegotiationSession.fromJson(Map<String, dynamic> json) {
    final Object? rawTurns = json['turns'];
    final List<NegotiationTurn> turns = rawTurns is List
        ? rawTurns
            .whereType<Map>()
            .map((Map<dynamic, dynamic> turn) =>
                NegotiationTurn.fromJson(_map(turn)))
            .toList(growable: false)
        : const <NegotiationTurn>[];
    final Object? rawArtifact = json['artifact'];
    return NegotiationSession(
      id: _text(json['id']) ?? '',
      topic: _text(json['topic']) ?? '',
      state: TaskState.parse(json['state']),
      round: _int(json['round']) ?? 0,
      tokens: _int(json['tokens']) ?? 0,
      initiatorAgentId: _text(json['initiatorAgentId']) ?? '',
      initiatorUserId: _text(json['initiatorUserId']) ?? '',
      responderAgentId: _text(json['responderAgentId']) ?? '',
      responderUserId: _text(json['responderUserId']) ?? '',
      turns: turns,
      terminalReason: _text(json['terminalReason']),
      artifact: rawArtifact is Map
          ? NegotiationArtifact.fromJson(_map(rawArtifact))
          : null,
    );
  }

  String get laneLabel => agentDisplayName(initiatorAgentId);

  String get stateLabel => state.label;
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
