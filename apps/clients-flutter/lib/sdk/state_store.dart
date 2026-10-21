import 'dart:async';

import 'package:flutter/foundation.dart';

import '../models/approval.dart';
import '../models/negotiation.dart';
import '../models/paid_message.dart';
import 'api_client.dart';

/// 连接状态。不要命名为 ConnectionState：与 Flutter widgets 同名。
enum LinkState { online, degraded, offline }

/// 单例状态仓库：轮询 `GET /api/state`，动作走 `POST`。
///
/// 设计约束（docs/07 §9）：
///  - 前台 5s / 后台 30s 轮询，失败后退避，不无限重试；
///  - 客户端不推断状态迁移，一切以服务端返回为准；
///  - 离线只读，不把审批动作排队（避免过期误操作）。
class StateStore extends ChangeNotifier {
  StateStore({
    required ApiClient api,
    this.pollInterval = const Duration(seconds: 5),
  }) : _api = api;

  final ApiClient _api;
  final Duration pollInterval;

  Timer? _timer;
  bool _syncing = false;
  bool _disposed = false;

  List<ApprovalTicket> _approvals = const <ApprovalTicket>[];
  List<NegotiationSession> _sessions = const <NegotiationSession>[];
  List<PaidMessage> _messages = const <PaidMessage>[];

  LinkState _link = LinkState.offline;
  DateTime? _lastSyncAt;
  String? _lastError;

  List<ApprovalTicket> get approvals => _approvals;
  List<NegotiationSession> get sessions => _sessions;
  List<PaidMessage> get messages => _messages;
  LinkState get link => _link;
  DateTime? get lastSyncAt => _lastSyncAt;
  String? get lastError => _lastError;
  bool get isSyncing => _syncing;

  /// 待决策的审批单（服务端已按 priority→deadline 排序，客户端不再重排）。
  List<ApprovalTicket> get pendingApprovals => _approvals
      .where((ApprovalTicket ticket) => ticket.state.isDecidable)
      .toList(growable: false);

  /// 按优先级分组用于列表分节（Dart Map 保持插入顺序）。
  Map<String, List<ApprovalTicket>> get pendingGroups {
    final List<ApprovalTicket> pending = pendingApprovals;
    final Map<String, List<ApprovalTicket>> groups =
        <String, List<ApprovalTicket>>{};
    for (final TicketPriority priority in TicketPriority.values) {
      final List<ApprovalTicket> items = pending
          .where((ApprovalTicket ticket) => ticket.priority == priority)
          .toList(growable: false);
      if (items.isNotEmpty) groups[priority.label] = items;
    }
    return groups;
  }

  List<ApprovalTicket> get runningTickets => _approvals
      .where((ApprovalTicket ticket) => ticket.state.isRunning)
      .toList(growable: false);

  /// 正在协商的会话（巡视态泳道）。
  List<NegotiationSession> get activeSessions => _sessions
      .where((NegotiationSession session) => !session.state.isTerminal)
      .toList(growable: false);

  /// 需要人介入的协商（INPUT_REQUIRED / AUTH_REQUIRED）。
  List<NegotiationSession> get pendingHumanSessions => _sessions
      .where((NegotiationSession session) => session.state.needsHuman)
      .toList(growable: false);

  /// 待处理的陌生人来意（预审通过、等待用户决策）。
  List<PaidMessage> get incomingMessages => _messages
      .where((PaidMessage message) =>
          message.state.canPay ||
          message.state.canView ||
          message.state.canReply)
      .toList(growable: false);

  ApprovalTicket? ticketById(String id) {
    for (final ApprovalTicket ticket in _approvals) {
      if (ticket.id == id) return ticket;
    }
    return null;
  }

  PaidMessage? messageById(String id) {
    for (final PaidMessage message in _messages) {
      if (message.id == id) return message;
    }
    return null;
  }

  NegotiationSession? sessionById(String id) {
    for (final NegotiationSession session in _sessions) {
      if (session.id == id) return session;
    }
    return null;
  }

  /// 找到与一条付费消息关联的审批单（演示数据用 `payload.messageId` 关联）。
  ///
  /// 来意四要素、预审结论、协商轮次都挂在审批单 payload 上，
  /// 因为快照里的 message 只保留业务字段。
  ApprovalTicket? ticketForMessage(String messageId) {
    for (final ApprovalTicket ticket in _approvals) {
      if (ticket.payload['messageId'] == messageId) return ticket;
    }
    return null;
  }

  void startPolling() {
    if (_disposed) return;
    _timer?.cancel();
    unawaited(refresh());
    _timer = Timer.periodic(pollInterval, (Timer _) {
      unawaited(refresh());
    });
  }

  void stopPolling() {
    _timer?.cancel();
    _timer = null;
  }

  Future<void> refresh() async {
    if (_syncing || _disposed) return;
    _syncing = true;
    notifyListeners();
    try {
      final ServerSnapshot snapshot = await _api.fetchState();
      _applySnapshot(snapshot);
      _link = LinkState.online;
      _lastError = null;
    } on ApiException catch (error) {
      _lastError = humanizeError(error);
      _link = _lastSyncAt == null ? LinkState.offline : LinkState.degraded;
    } finally {
      _syncing = false;
      if (!_disposed) notifyListeners();
    }
  }

  /// 审批动作。服务端返回的 snapshot 是唯一状态源，客户端不本地改状态。
  Future<void> decide({
    required String ticketId,
    required DecisionAction action,
    Map<String, dynamic>? patch,
    String? comment,
  }) async {
    try {
      final DecideOutcome outcome = await _api.decide(
        ticketId: ticketId,
        action: action,
        patch: patch,
        comment: comment,
      );
      final ServerSnapshot? snapshot = outcome.snapshot;
      if (snapshot != null) {
        _applySnapshot(snapshot);
      } else {
        await refresh();
      }
      _link = LinkState.online;
      _lastError = null;
      notifyListeners();
    } on ApiException catch (error) {
      _lastError = humanizeError(error);
      _link = LinkState.degraded;
      notifyListeners();
      rethrow;
    }
  }

  void _applySnapshot(ServerSnapshot snapshot) {
    _approvals = snapshot.approvals;
    _sessions = snapshot.sessions;
    _messages = snapshot.messages;
    _lastSyncAt = DateTime.now();
  }

  Future<PaidMessage> payMessage(String messageId) async {
    try {
      final PaidMessage updated = await _api.payIntent(messageId);
      _replaceMessage(updated);
      notifyListeners();
      return updated;
    } on ApiException catch (error) {
      _lastError = humanizeError(error);
      notifyListeners();
      rethrow;
    }
  }

  Future<PaidMessage> viewMessage(String messageId) async {
    try {
      final PaidMessage updated = await _api.viewIntent(messageId);
      _replaceMessage(updated);
      notifyListeners();
      return updated;
    } on ApiException catch (error) {
      _lastError = humanizeError(error);
      notifyListeners();
      rethrow;
    }
  }

  Future<PaidMessage> replyMessage(String messageId, String replyText) async {
    try {
      final PaidMessage updated =
          await _api.replyIntent(messageId, replyText: replyText);
      _replaceMessage(updated);
      notifyListeners();
      return updated;
    } on ApiException catch (error) {
      _lastError = humanizeError(error);
      notifyListeners();
      rethrow;
    }
  }

  void _replaceMessage(PaidMessage message) {
    _messages = _messages
        .map((PaidMessage item) => item.id == message.id ? message : item)
        .toList(growable: false);
  }

  @override
  void dispose() {
    _disposed = true;
    stopPolling();
    super.dispose();
  }
}

/// 契约错误码 -> 用户可读文案（docs/07 §3.3）。
String humanizeError(ApiException error) {
  switch (error.code) {
    case 'FORBIDDEN':
      return '这不是你的单据，无法操作。';
    case 'EXPIRED_TICKET':
      return '这张审批单已超时，请刷新后查看。';
    case 'NOT_FOUND':
      return '单据不存在或已被撤回。';
    case 'ILLEGAL_TRANSITION':
      return '状态已变化，请刷新后重试。';
    case 'NOT_PAYABLE':
      return '这条消息未通过预审，不能付费。';
    case 'NEEDS_SCREENING':
      return '对方还在说明来意，稍后再看。';
    case 'DAILY_CAP_EXCEEDED':
      return '今日发送已到上限。';
    case 'SELF_MESSAGE':
      return '不能给自己发付费消息。';
    case 'TIMEOUT':
    case 'NETWORK':
      return error.message;
    default:
      return '操作失败：${error.message}';
  }
}
