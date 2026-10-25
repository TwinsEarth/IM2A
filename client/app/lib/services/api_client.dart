// v1.3.4/v1.6: 真实 API 客户端，对接 gateway 全部路由。非 2xx 抛异常（不做假成功）。
import "dart:convert";
import "dart:io";

class ApiClient {
  final String baseUrl;
  final String token;
  ApiClient({required this.baseUrl, required this.token});

  Map<String, String> get _hdr => {
    "Authorization": "Bearer $token",
    "Content-Type": "application/json",
  };

  Future<Map<String, dynamic>> _post(String path, Map body) async {
    final c = HttpClient();
    try {
      final req = await c.postUrl(Uri.parse("$baseUrl$path"));
      _hdr.forEach(req.headers.set);
      req.write(jsonEncode(body));
      final resp = await req.close();
      final bodyStr = await resp.transform(utf8.decoder).join();
      if (resp.statusCode >= 400) {
        throw HttpException("POST $path -> ${resp.statusCode}: $bodyStr");
      }
      return jsonDecode(bodyStr) as Map<String, dynamic>;
    } finally {
      c.close();
    }
  }

  Future<Map<String, dynamic>> _get(String path) async {
    final c = HttpClient();
    try {
      final req = await c.getUrl(Uri.parse("$baseUrl$path"));
      _hdr.forEach(req.headers.set);
      final resp = await req.close();
      final bodyStr = await resp.transform(utf8.decoder).join();
      if (resp.statusCode >= 400) {
        throw HttpException("GET $path -> ${resp.statusCode}: $bodyStr");
      }
      return jsonDecode(bodyStr) as Map<String, dynamic>;
    } finally {
      c.close();
    }
  }

  // 审批
  Future<Map> approve(String ticketId, String note) =>
      _post("/v1/approvals/$ticketId/approve", {"note": note});
  Future<Map> reject(String ticketId, String reason) =>
      _post("/v1/approvals/$ticketId/reject", {"reason": reason});
  Future<Map> modify(String ticketId, String patch) =>
      _post("/v1/approvals/$ticketId/modify", {"patch": patch});

  // v1.6.0：审批态真实待办（来自审批引擎）
  Future<List<Map<String, dynamic>>> pendingApprovals(int user) async {
    final m = await _get("/v1/approvals/pending?user=$user");
    return ((m["tickets"] as List?) ?? const []).cast<Map<String, dynamic>>();
  }

  // 信任（v1.6 修复：后端路径为 /v1/trust/grant，原 /v1/trust/ 会 404）
  Future<Map> setTrust(String contactId, int level, List<String> caps) =>
      _post("/v1/trust/grant", {"contactId": contactId, "trustLevel": level, "capabilities": caps});
  Future<Map> checkCapability(String targetId, String tool) =>
      _post("/v1/capability/check", {"targetId": targetId, "tool": tool});

  // LLM
  Future<Map> precheck(String text) => _post("/v1/intent/precheck", {"text": text});
  Future<Map> guardReview(String text) => _post("/v1/guard/review", {"text": text});
  Future<Map> route(String text) => _post("/v1/route/", {"text": text});
  Future<Map> distill(String agentId, String decision, String ctx, String fb) =>
      _post("/v1/distill/feedback", {"agentId": agentId, "decision": decision, "context": ctx, "feedback": fb});

  // 付费
  Future<Map> charge(String mid, String sender, String receiver, int cents) =>
      _post("/v1/ledger/charge", {"messageId": mid, "senderId": sender, "receiverId": receiver, "amountCents": cents, "mode": "send"});
  Future<Map> ledger(String userId) => _get("/v1/ledger/?userId=$userId");

  // v1.6.4 巡视态：真实 Agent 工作日志（原 events() 调 /v1/events/ 会 404，已废弃）
  Future<List<Map<String, dynamic>>> agentLogs(String userId) async {
    final m = await _get("/v1/agent/logs?userId=$userId");
    return ((m["entries"] as List?) ?? const []).cast<Map<String, dynamic>>();
  }

  // v1.3.3 会话态：人类消息（最终对接腾讯云 IM；当前走网关消息服务，真实落 PG）
  Future<Map> sendMessage(int senderId, int receiverId, String content) =>
      _post("/v1/msg/send", {"senderId": senderId, "receiverId": receiverId, "content": content});
  Future<List<Map<String, dynamic>>> listMessages(int userId, {int limit = 50}) async {
    final m = await _get("/v1/msg/list?userId=$userId&limit=$limit");
    return ((m["messages"] as List?) ?? const []).cast<Map<String, dynamic>>();
  }

  // v1.3.2 协商时间线
  Future<List<Map<String, dynamic>>> negotiations({int limit = 50}) async {
    final m = await _get("/v1/negotiations/list?limit=$limit");
    return ((m["negotiations"] as List?) ?? const []).cast<Map<String, dynamic>>();
  }

  // v1.3.1 超时升级（deadlineMs 为截止时间毫秒；仅到期且仍 pending 才会真正升级）
  Future<Map> escalate(String ticketId, int deadlineMs) =>
      _post("/v1/watch/escalate", {"ticketId": ticketId, "deadlineMs": deadlineMs});

  // v1.5 多端同步 / 设备 / 离线
  Future<Map> registerDevice(String deviceId, String type) =>
      _post("/v1/device/register", {"deviceId": deviceId, "type": type});
  Future<Map> syncState(String ticketId, String decision, int clientTs) =>
      _post("/v1/sync/state", {"ticketId": ticketId, "decision": decision, "clientTs": clientTs});
  Future<Map> offlineSubmit(String ticketId, String decision) =>
      _post("/v1/offline/submit", {"ticketId": ticketId, "decision": decision});

  // 健康
  Future<Map> health() => _get("/healthz");
}
