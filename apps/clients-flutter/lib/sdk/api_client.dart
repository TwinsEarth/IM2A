import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import '../models/approval.dart';
import '../models/negotiation.dart';
import '../models/paid_message.dart';

/// 服务端一次状态快照，对应 `GET /api/state`。
///
/// 演示服务的真实字段：`approvals` / `negotiations` / `messages` / `trace` /
/// `checks` / `audit` / `meta`；本客户端只取前三者。
class ServerSnapshot {
  const ServerSnapshot({
    required this.approvals,
    required this.sessions,
    required this.messages,
    required this.serverTime,
    required this.healthy,
  });

  final List<ApprovalTicket> approvals;
  final List<NegotiationSession> sessions;
  final List<PaidMessage> messages;
  final DateTime? serverTime;
  final bool healthy;

  static const ServerSnapshot empty = ServerSnapshot(
    approvals: <ApprovalTicket>[],
    sessions: <NegotiationSession>[],
    messages: <PaidMessage>[],
    serverTime: null,
    healthy: false,
  );
}

/// 审批动作的结果。
///
/// 演示服务的 `POST /api/approvals/{id}/decide` 返回
/// `{ ok, ticketId, state, result, messageState, snapshot }`，
/// 因此客户端直接消费其中的 `snapshot`，不自己推断状态迁移。
class DecideOutcome {
  const DecideOutcome({
    required this.ticketId,
    required this.state,
    required this.messageState,
    required this.snapshot,
  });

  final String ticketId;
  final TicketState? state;
  final MsgState? messageState;
  final ServerSnapshot? snapshot;
}

/// 接口错误，携带契约错误码：
/// `FORBIDDEN` / `EXPIRED_TICKET` / `NOT_FOUND` / `ILLEGAL_TRANSITION` /
/// `NOT_PAYABLE` / `NEEDS_SCREENING` / `DAILY_CAP_EXCEEDED` / `SELF_MESSAGE`。
class ApiException implements Exception {
  const ApiException(this.code, this.message, {this.statusCode = 0});

  final String code;
  final String message;
  final int statusCode;

  @override
  String toString() => 'ApiException($code, http=$statusCode): $message';
}

/// 最小 REST 客户端。
///
/// 路由（与 `apps/demo-server/server.mjs` 一致）：
///   GET  /api/health
///   GET  /api/state
///   POST /api/approvals/{id}/decide     body: { action, comment? }
///   POST /api/reset                     重置演示世界
///
/// 下列路由属于契约规划（`contracts/openapi`），演示服务**尚未实现**，调用会得到 404：
///   POST /api/intent
///   POST /api/intent/{id}/screen|pay|view|reply
class ApiClient {
  ApiClient({
    Uri? baseUri,
    http.Client? httpClient,
    this.timeout = const Duration(seconds: 10),
  })  : baseUri = baseUri ?? Uri.parse('http://127.0.0.1:8787'),
        _http = httpClient ?? http.Client();

  final Uri baseUri;
  final Duration timeout;
  final http.Client _http;

  Uri _uri(String path) => baseUri.resolve(path);

  Future<ServerSnapshot> fetchState() async {
    final Map<String, dynamic> body = await _get('/api/state');
    return _snapshotFrom(body);
  }

  /// 审批动作。服务端是唯一状态源：返回结果里带整份 snapshot。
  Future<DecideOutcome> decide({
    required String ticketId,
    required DecisionAction action,
    Map<String, dynamic>? patch,
    String? comment,
  }) async {
    final Map<String, dynamic> body =
        await _post('/api/approvals/$ticketId/decide', <String, dynamic>{
      'action': action.wire,
      if (patch != null) 'patch': patch,
      if (comment != null) 'comment': comment,
    });
    final Object? rawSnapshot = body['snapshot'];
    return DecideOutcome(
      ticketId: _text(body['ticketId']) ?? ticketId,
      state: _ticketStateOrNull(body['state']),
      messageState: _messageStateOrNull(body['messageState']),
      snapshot: rawSnapshot is Map ? _snapshotFrom(_map(rawSnapshot)) : null,
    );
  }

  /// 陌生人发起来意（契约规划，演示服务未实现）。
  Future<PaidMessage> submitIntent({
    required String senderUserId,
    required String senderAgentId,
    required String receiverUserId,
    required String topic,
    required String summary,
    required BillingMode billingMode,
    required int amountCents,
    required String idempotencyKey,
  }) async {
    final Map<String, dynamic> body = await _post('/api/intent', <String, dynamic>{
      'senderUserId': senderUserId,
      'senderAgentId': senderAgentId,
      'receiverUserId': receiverUserId,
      'topic': topic,
      'summary': summary,
      'billingMode': billingMode.code,
      'amountCents': amountCents,
      'idempotencyKey': idempotencyKey,
    });
    return _messageFrom(body);
  }

  /// 目标用户 Agent 预审结论（必须先于 pay）。
  Future<PaidMessage> screenIntent({
    required String messageId,
    required bool passed,
  }) async {
    final Map<String, dynamic> body =
        await _post('/api/intent/$messageId/screen', <String, dynamic>{
      'verdict': passed ? 'PASS' : 'BLOCK',
    });
    return _messageFrom(body);
  }

  /// 付费（服务端幂等键 `pay:<messageId>`，重复调用不得二次扣费）。
  Future<PaidMessage> payIntent(String messageId) async {
    final Map<String, dynamic> body =
        await _post('/api/intent/$messageId/pay', const <String, dynamic>{});
    return _messageFrom(body);
  }

  Future<PaidMessage> viewIntent(String messageId) async {
    final Map<String, dynamic> body =
        await _post('/api/intent/$messageId/view', const <String, dynamic>{});
    return _messageFrom(body);
  }

  Future<PaidMessage> replyIntent(String messageId, {String? replyText}) async {
    final Map<String, dynamic> body =
        await _post('/api/intent/$messageId/reply', <String, dynamic>{
      if (replyText != null) 'replyText': replyText,
    });
    return _messageFrom(body);
  }

  Future<bool> ping() async {
    try {
      await _get('/api/health');
      return true;
    } on ApiException {
      return false;
    }
  }

  void close() => _http.close();

  ServerSnapshot _snapshotFrom(Map<String, dynamic> body) {
    final Map<String, dynamic> state =
        body['state'] is Map ? _map(body['state']) : body;
    // 演示服务把协商放在 `negotiations`，契约/未来网关可能叫 `sessions`。
    final Object? rawSessions = state['sessions'] ?? state['negotiations'];
    return ServerSnapshot(
      approvals: _list(state['approvals'])
          .map((Object? raw) => ApprovalTicket.fromJson(_map(raw)))
          .toList(growable: false),
      sessions: _list(rawSessions)
          .map((Object? raw) => NegotiationSession.fromJson(_map(raw)))
          .toList(growable: false),
      messages: _list(state['messages'])
          .map((Object? raw) => PaidMessage.fromJson(_map(raw)))
          .toList(growable: false),
      serverTime: _dateOrNull(
        state['serverTime'] ?? _map(state['meta'])['generatedAt'],
      ),
      healthy: true,
    );
  }

  PaidMessage _messageFrom(Map<String, dynamic> body) {
    final Object? raw = body['message'] ?? body['data'] ?? body;
    return PaidMessage.fromJson(_map(raw));
  }

  Future<Map<String, dynamic>> _get(String path) async {
    try {
      final http.Response response = await _http.get(_uri(path)).timeout(timeout);
      return _decode(response);
    } on TimeoutException {
      throw const ApiException('TIMEOUT', '请求超时，请检查网络后重试');
    } on http.ClientException catch (error) {
      throw ApiException('NETWORK', '无法连接服务端：${error.message}');
    }
  }

  Future<Map<String, dynamic>> _post(
    String path,
    Map<String, dynamic> body,
  ) async {
    try {
      final http.Response response = await _http
          .post(
            _uri(path),
            headers: const <String, String>{
              'content-type': 'application/json; charset=utf-8',
            },
            body: jsonEncode(body),
          )
          .timeout(timeout);
      return _decode(response);
    } on TimeoutException {
      throw const ApiException('TIMEOUT', '请求超时，请稍后重试（不会重复扣费）');
    } on http.ClientException catch (error) {
      throw ApiException('NETWORK', '无法连接服务端：${error.message}');
    }
  }

  Map<String, dynamic> _decode(http.Response response) {
    final String text = utf8.decode(response.bodyBytes);
    Map<String, dynamic> body = const <String, dynamic>{};
    if (text.isNotEmpty) {
      try {
        final Object? decoded = jsonDecode(text);
        if (decoded is Map) body = _map(decoded);
      } on FormatException {
        // 非 JSON（网关错误页等），按状态码抛出。
      }
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      return body;
    }
    // 演示服务的错误体是 `{ error: 'CODE', message: '...' }`（error 为字符串），
    // 契约网关可能是 `{ error: { code, message } }`，两者都兼容。
    final Object? rawError = body['error'];
    final Map<String, dynamic> nested =
        rawError is Map ? _map(rawError) : body;
    final String code = rawError is String
        ? rawError
        : (_text(nested['code']) ?? 'HTTP_${response.statusCode}');
    final String message = _text(nested['message']) ??
        _text(body['message']) ??
        '请求失败（HTTP ${response.statusCode}）';
    throw ApiException(code, message, statusCode: response.statusCode);
  }
}

String? _text(Object? value) {
  if (value == null) return null;
  final String text = '$value';
  return text.isEmpty ? null : text;
}

List<dynamic> _list(Object? value) =>
    value is List ? value : const <dynamic>[];

Map<String, dynamic> _map(Object? value) {
  if (value is Map) {
    return value.map((Object? key, Object? val) =>
        MapEntry<String, dynamic>('$key', val));
  }
  return const <String, dynamic>{};
}

DateTime? _dateOrNull(Object? value) {
  if (value is String) {
    final DateTime? parsed = DateTime.tryParse(value);
    if (parsed != null) return parsed.toLocal();
  }
  return null;
}

TicketState? _ticketStateOrNull(Object? raw) {
  if (raw is String) {
    for (final TicketState state in TicketState.values) {
      if (state.wire == raw) return state;
    }
  }
  return null;
}

MsgState? _messageStateOrNull(Object? raw) {
  if (raw is String) {
    for (final MsgState state in MsgState.values) {
      if (state.wire == raw) return state;
    }
  }
  return null;
}
