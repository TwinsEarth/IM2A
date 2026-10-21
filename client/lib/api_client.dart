// 模块职责：im2agent 客户端 API 层 —— 只依赖 /api/admin/state 与 /api/approval/decide 等少量接口
// 后端契约见 apps/im2agent/server.mjs 头注释
import 'dart:convert';

import 'package:http/http.dart' as http;

class ApiException implements Exception {
  ApiException(this.status, this.code, this.message);

  final int status;
  final String code;
  final String message;

  @override
  String toString() => '($status) $code: $message';
}

class ApiClient {
  ApiClient({required this.baseUrl, required this.token, http.Client? httpClient})
      : _http = httpClient ?? http.Client();

  final String baseUrl;
  final String token;
  final http.Client _http;

  Map<String, String> get _headers => {
        'content-type': 'application/json',
        if (token.isNotEmpty) 'x-admin-token': token,
      };

  Uri _uri(String path) => Uri.parse('${baseUrl.replaceAll(RegExp(r'/$'), '')}$path');

  Future<Map<String, dynamic>> _json(http.Response response) async {
    final body = response.body.isEmpty ? <String, dynamic>{} : jsonDecode(response.body) as Map<String, dynamic>;
    if (response.statusCode >= 400) {
      throw ApiException(response.statusCode, (body['error'] ?? 'ERROR').toString(), (body['message'] ?? '').toString());
    }
    return body;
  }

  Future<Map<String, dynamic>> health() async => _json(await _http.get(_uri('/api/health')));

  /// 运维全量状态：Agent 注册表 / 审批单 / 付费消息 / 审计链
  Future<Map<String, dynamic>> adminState() async => _json(await _http.get(_uri('/api/admin/state'), headers: _headers));

  Future<Map<String, dynamic>> ledger() async => _json(await _http.get(_uri('/api/ledger'), headers: _headers));

  Future<Map<String, dynamic>> audit() async => _json(await _http.get(_uri('/api/audit'), headers: _headers));

  /// 人对审批单做决策
  Future<Map<String, dynamic>> decide(String ticketId, String action, {String? comment}) async {
    final response = await _http.post(
      _uri('/api/approval/decide'),
      headers: _headers,
      body: jsonEncode({'ticketId': ticketId, 'action': action, if (comment != null) 'comment': comment}),
    );
    return _json(response);
  }

  Future<Map<String, dynamic>> draft(String text, {String? topic}) async {
    final response = await _http.post(
      _uri('/api/llm/draft'),
      headers: _headers,
      body: jsonEncode({'text': text, if (topic != null) 'topic': topic}),
    );
    return _json(response);
  }
}
