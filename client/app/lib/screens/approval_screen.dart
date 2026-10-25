// v1.6.0/v1.6.3: 审批态——真实待办拉取 + SSE 实时推送卡片流。
import "dart:async";
import "package:flutter/material.dart";
import "../models/approval_ticket.dart";
import "../services/api_client.dart";
import "../services/sse_client.dart";
import "../widgets/approval_card.dart";

class ApprovalScreen extends StatefulWidget {
  const ApprovalScreen({super.key});
  @override
  State<ApprovalScreen> createState() => _ApprovalScreenState();
}

class _ApprovalScreenState extends State<ApprovalScreen> {
  // 登录后由账号体系注入；当前演示使用数字用户 1。
  static const String userId = "1";
  // demo 占位：本地网关地址；生产地址属于部署配置，禁止在此填写真实生产地址。
  final ApiClient api = ApiClient(baseUrl: "http://127.0.0.1:18080", token: "im2a_demo_token");
  final SseClient sse = SseClient(baseUrl: "http://127.0.0.1:18080", token: "im2a_demo_token");
  StreamSubscription? _sub;
  List<ApprovalTicket> _tickets = [];
  String _error = "";
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _load();
    sse.start(userId);
    _sub = sse.events.listen((event) {
      if (!mounted) return;
      setState(() {
        _tickets.add(ApprovalTicket(
          id: event["ticketId"] ?? event["ticket"] ?? "new",
          title: event["title"] ?? "新审批",
          summary: event.toString(),
          agentId: event["agent"] ?? "butler",
          state: "PENDING",
          deadline: DateTime.now().add(const Duration(hours: 2)),
          payload: const {},
        ));
      });
    });
  }

  // 从后端真实拉取待办，不再使用硬编码卡片。
  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      final rows = await api.pendingApprovals(int.parse(userId));
      if (!mounted) return;
      setState(() {
        _tickets = rows.map(_fromJson).toList();
        _error = "";
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() { _error = "$e"; _loading = false; });
    }
  }

  ApprovalTicket _fromJson(Map<String, dynamic> t) {
    final deadlineUnix = t["deadlineUnix"];
    return ApprovalTicket(
      id: t["id"] ?? "",
      title: t["type"] ?? "审批",
      summary: "状态: ${t["state"] ?? "PENDING"}",
      agentId: t["sourceAgentId"] ?? "butler",
      state: t["state"] ?? "PENDING",
      deadline: deadlineUnix is int
          ? DateTime.fromMillisecondsSinceEpoch(deadlineUnix * 1000)
          : DateTime.now().add(const Duration(minutes: 30)),
      payload: (t["payload"] as Map?)?.cast<String, dynamic>() ?? const {},
    );
  }

  // v1.3.1 修改：弹窗收集修改意见，真实回退给 Agent（/v1/approvals/{id}/modify）。
  Future<void> _modify(ApprovalTicket t) async {
    final ctrl = TextEditingController();
    final ok = await showDialog<bool>(
      context: context,
      builder: (_) => AlertDialog(
        title: Text("修改：${t.title}"),
        content: TextField(
          controller: ctrl,
          maxLines: 3,
          decoration: const InputDecoration(
            hintText: "填写修改意见，方案将回退给 Agent 重新协商"),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false),
              child: const Text("取消")),
          FilledButton(onPressed: () => Navigator.pop(context, true),
              child: const Text("提交修改")),
        ],
      ),
    );
    if (ok == true && ctrl.text.trim().isNotEmpty) {
      try {
        await api.modify(t.id, ctrl.text.trim());
        if (mounted) await _load();
      } catch (e) {
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("修改失败：$e")));
        }
      }
    }
  }

  // v1.3.1 超时升级：仅在审批单已过 deadline 时，后端才会真正升级到短信/电话。
  Future<void> _escalate(ApprovalTicket t) async {
    try {
      await api.escalate(t.id, t.deadline.millisecondsSinceEpoch);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text("已请求紧急升级（短信/电话通道）")));
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("升级失败：$e")));
      }
    }
  }

  @override
  void dispose() {
    _sub?.cancel();
    sse.close();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text("待审批"), centerTitle: true),
      body: RefreshIndicator(
        onRefresh: _load,
        child: _loading
            ? const Center(child: CircularProgressIndicator())
            : _error.isNotEmpty
                ? ListView(children: [Padding(padding: const EdgeInsets.all(24),
                    child: Text("加载失败：$_error", style: const TextStyle(color: Colors.red)))])
                : _tickets.isEmpty
                    ? const Center(child: Text("暂无待办"))
                    : ListView.builder(
                        itemCount: _tickets.length,
                        itemBuilder: (_, i) => ApprovalCard(
                          ticket: _tickets[i],
                          onApprove: () async {
                            await api.approve(_tickets[i].id, "通过");
                            if (mounted) setState(() => _tickets.removeAt(i));
                          },
                          onReject: () async {
                            await api.reject(_tickets[i].id, "驳回");
                            if (mounted) setState(() => _tickets.removeAt(i));
                          },
                          onModify: () => _modify(_tickets[i]),
                          onEscalate: () => _escalate(_tickets[i]),
                        ),
                      ),
      ),
    );
  }
}
