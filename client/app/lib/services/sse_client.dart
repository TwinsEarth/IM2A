// v1.6.3: SSE 实时审批流客户端——带断线自动重连与事件类型解析。
import "dart:async";
import "dart:convert";
import "dart:io";

class SseClient {
  final String baseUrl;
  final String token;
  HttpClient? _client;
  bool _closed = false;
  int _backoffMs = 500;
  final StreamController<Map<String, dynamic>> _ctrl =
      StreamController<Map<String, dynamic>>.broadcast();

  SseClient({required this.baseUrl, required this.token});

  /// 业务审批事件流（已过滤心跳与 connected 握手）。
  Stream<Map<String, dynamic>> get events => _ctrl.stream;

  void start(String userId) {
    _loop(userId);
  }

  Future<void> _loop(String userId) async {
    while (!_closed) {
      try {
        _client = HttpClient();
        final req = await _client!.getUrl(Uri.parse("$baseUrl/v1/sse/?userId=$userId"));
        req.headers.set("Authorization", "Bearer $token");
        final resp = await req.close();
        if (resp.statusCode != 200) {
          throw HttpException("SSE ${resp.statusCode}");
        }
        _backoffMs = 500; // 连接成功后重置退避
        String? eventType;
        await for (final line
            in resp.transform(utf8.decoder).transform(const LineSplitter())) {
          if (line.startsWith("event:")) {
            eventType = line.substring(6).trim();
          } else if (line.startsWith("data:") && eventType == "approval") {
            final payload = line.substring(5).trim();
            if (payload.isNotEmpty) {
              try {
                _ctrl.add(jsonDecode(payload) as Map<String, dynamic>);
              } catch (_) {}
            }
          } else if (line.isEmpty) {
            eventType = null;
          }
        }
      } catch (_) {
        // 断线：落入下方指数退避后重连
      }
      if (_closed) break;
      await Future.delayed(Duration(milliseconds: _backoffMs));
      _backoffMs = (_backoffMs * 2).clamp(500, 15000);
    }
  }

  void close() {
    _closed = true;
    _client?.close(force: true);
    if (!_ctrl.isClosed) _ctrl.close();
  }
}
