// v1.3.3: 会话态——Agent 摘要层 IM + 人类消息分流（全部真实拉取/发送，无硬编码）。
// 人类情感消息走人类通道（最终对接腾讯云 IM，当前走网关消息服务）；
// Agent 协商结果以摘要/审批卡片形式注入，二者物理分流。
import "package:flutter/material.dart";
import "../services/api_client.dart";

class ChatMsg {
  final String from;
  final String text;
  final int kind; // 0=人类消息 1=Agent 摘要 2=审批卡片
  final bool mine;
  ChatMsg.human({required this.from, required this.text, required this.mine}) : kind = 0;
  ChatMsg.agent({required this.from, required this.text})
      : kind = 1, mine = false;
  ChatMsg.approval({required this.from, required this.text})
      : kind = 2, mine = false;
}

class ConversationScreen extends StatefulWidget {
  const ConversationScreen({super.key});
  @override
  State<ConversationScreen> createState() => _ConversationScreenState();
}

class _ConversationScreenState extends State<ConversationScreen> {
  // 登录后由账号体系注入；演示环境当前对话对端为 2。
  static const int userId = 1;
  static const int peerId = 2;
  // demo 占位：本地网关地址；生产地址属于部署配置，禁止在此填写真实生产地址。
  final ApiClient api = ApiClient(baseUrl: "http://127.0.0.1:18080", token: "im2a_demo_token");
  final TextEditingController _input = TextEditingController();
  final List<ChatMsg> _msgs = [];
  String _error = "";
  bool _loading = true;
  bool _sending = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      final results = await Future.wait([
        api.listMessages(userId, limit: 50),
        api.negotiations(limit: 10),
        api.pendingApprovals(userId),
      ]);
      final msgs = results[0] as List<Map<String, dynamic>>;
      final negs = results[1] as List<Map<String, dynamic>>;
      final pending = results[2] as List<Map<String, dynamic>>;
      final list = <ChatMsg>[];
      // ① 待审批卡片（Agent 协商收敛后的最终结果）
      for (final t in pending) {
        list.add(ChatMsg.approval(
          from: "${t["sourceAgentId"] ?? "agent"}",
          text: "${t["type"] ?? "审批"}：待你裁决（去“审批”页处理）",
        ));
      }
      // ② Agent 协商摘要
      for (final n in negs) {
        final round = n["round"] ?? 0;
        list.add(ChatMsg.agent(
          from: "negotiation",
          text: "Agent 协商 ${n["id"]}：状态 ${n["state"]}，已进行 $round 轮。",
        ));
      }
      // ③ 人类消息（真实落库）
      for (final m in msgs) {
        final sender = (m["senderId"] is int) ? m["senderId"] as int : 0;
        list.add(ChatMsg.human(
          from: "用户$sender",
          text: "${m["content"] ?? ""}",
          mine: sender == userId,
        ));
      }
      if (!mounted) return;
      setState(() {
        _msgs
          ..clear()
          ..addAll(list);
        _error = "";
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() { _error = "$e"; _loading = false; });
    }
  }

  Future<void> _send() async {
    final text = _input.text.trim();
    if (text.isEmpty || _sending) return;
    setState(() => _sending = true);
    try {
      await api.sendMessage(userId, peerId, text);
      _input.clear();
      await _load();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text("发送失败：$e")));
      }
    } finally {
      if (mounted) setState(() => _sending = false);
    }
  }

  @override
  void dispose() {
    _input.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text("会话"), centerTitle: true,
        actions: [IconButton(onPressed: _load, icon: const Icon(Icons.refresh))]),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _error.isNotEmpty
              ? Center(child: Text("加载失败：$_error", style: const TextStyle(color: Colors.red)))
              : Column(children: [
                  Expanded(child: RefreshIndicator(
                    onRefresh: _load,
                    child: _msgs.isEmpty
                        ? ListView(children: const [
                            SizedBox(height: 120),
                            Center(child: Text("暂无消息"))])
                        : ListView.builder(
                            padding: const EdgeInsets.all(12),
                            itemCount: _msgs.length,
                            itemBuilder: (_, i) => _bubble(_msgs[i]),
                          ),
                  )),
                  Padding(padding: const EdgeInsets.all(8), child: Row(children: [
                    Expanded(child: TextField(
                      controller: _input,
                      onSubmitted: (_) => _send(),
                      decoration: InputDecoration(
                        hintText: "发消息（人类通道）",
                        border: OutlineInputBorder(borderRadius: BorderRadius.circular(24)),
                        contentPadding: const EdgeInsets.symmetric(horizontal: 16)))),
                    const SizedBox(width: 8),
                    IconButton.filled(
                      onPressed: _sending ? null : _send,
                      icon: _sending
                          ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2))
                          : const Icon(Icons.send)),
                  ])),
                ]),
    );
  }

  Widget _bubble(ChatMsg m) {
    if (m.kind == 2) {
      return Card(
        color: Colors.blue[50],
        child: ListTile(
          leading: const Icon(Icons.rule, color: Colors.blue),
          title: Text(m.text),
          subtitle: Text(m.from),
          trailing: const Text("去审批", style: TextStyle(color: Colors.blue)),
        ),
      );
    }
    if (m.kind == 1) {
      return Align(
        alignment: Alignment.centerLeft,
        child: Container(
          margin: const EdgeInsets.symmetric(vertical: 4),
          padding: const EdgeInsets.all(10),
          decoration: BoxDecoration(
            color: Colors.grey[200], borderRadius: BorderRadius.circular(8)),
          child: Row(mainAxisSize: MainAxisSize.min, children: [
            const Icon(Icons.smart_toy, size: 16, color: Colors.grey),
            const SizedBox(width: 6),
            Flexible(child: Text(m.text)),
          ]),
        ),
      );
    }
    return Align(
      alignment: m.mine ? Alignment.centerRight : Alignment.centerLeft,
      child: Container(
        margin: const EdgeInsets.symmetric(vertical: 4),
        padding: const EdgeInsets.all(10),
        decoration: BoxDecoration(
          color: m.mine ? Colors.blue[100] : Colors.grey[300],
          borderRadius: BorderRadius.circular(8)),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(m.from, style: TextStyle(fontSize: 11, color: Colors.grey[700])),
          Text(m.text),
        ]),
      ),
    );
  }
}
