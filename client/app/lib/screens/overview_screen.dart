// v1.3.2: 巡视态——Agent 工作轨迹/审计事件流（/v1/agent/logs）+ 协商时间线（/v1/negotiations/list），全部真实。
import "package:flutter/material.dart";
import "../services/api_client.dart";

class AgentLog {
  final String agent;
  final String action;
  final String detail;
  final String time;
  AgentLog({required this.agent, required this.action, required this.detail, required this.time});
}

class NegItem {
  final String id;
  final String state;
  final int round;
  NegItem({required this.id, required this.state, required this.round});
}

class OverviewScreen extends StatefulWidget {
  const OverviewScreen({super.key});
  @override
  State<OverviewScreen> createState() => _OverviewScreenState();
}

class _OverviewScreenState extends State<OverviewScreen>
    with SingleTickerProviderStateMixin {
  static const String userId = "1";
  // demo 占位：本地网关地址；生产地址属于部署配置，禁止在此填写真实生产地址。
  final ApiClient api = ApiClient(baseUrl: "http://127.0.0.1:18080", token: "im2a_demo_token");
  late final TabController _tab = TabController(length: 2, vsync: this);
  List<AgentLog> _logs = [];
  List<NegItem> _negs = [];
  String _error = "";
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _load();
  }

  String _fmtTime(dynamic iso) {
    final s = "$iso";
    final t = s.indexOf("T");
    if (t >= 0 && s.length >= t + 6) return s.substring(t + 1, t + 6);
    return s;
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      final results = await Future.wait([
        api.agentLogs(userId),
        api.negotiations(limit: 50),
      ]);
      final logRows = results[0] as List<Map<String, dynamic>>;
      final negRows = results[1] as List<Map<String, dynamic>>;
      if (!mounted) return;
      setState(() {
        _logs = logRows.map((e) => AgentLog(
          agent: "${e["agent"] ?? "-"}",
          action: "${e["action"] ?? ""}",
          detail: "${e["detail"] ?? ""}",
          time: _fmtTime(e["time"]),
        )).toList();
        _negs = negRows.map((n) => NegItem(
          id: "${n["id"] ?? ""}",
          state: "${n["state"] ?? ""}",
          round: (n["round"] is int) ? n["round"] as int : 0,
        )).toList();
        _error = "";
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() { _error = "$e"; _loading = false; });
    }
  }

  @override
  void dispose() {
    _tab.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text("Agent 团队"), centerTitle: true,
        actions: [IconButton(onPressed: _load, icon: const Icon(Icons.refresh))],
        bottom: TabBar(controller: _tab, tabs: const [
          Tab(text: "工作轨迹 / 审计"),
          Tab(text: "协商时间线"),
        ]),
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _error.isNotEmpty
              ? Center(child: Text("加载失败：$_error", style: const TextStyle(color: Colors.red)))
              : TabBarView(controller: _tab, children: [
                  _logs.isEmpty
                      ? const Center(child: Text("暂无工作日志"))
                      : RefreshIndicator(onRefresh: _load, child: ListView.separated(
                          itemCount: _logs.length,
                          separatorBuilder: (_, _) => const Divider(height: 1),
                          itemBuilder: (_, i) {
                            final l = _logs[i];
                            return ListTile(
                              leading: CircleAvatar(child: Text(l.agent.substring(0, 1).toUpperCase())),
                              title: Text("${l.agent} · ${l.action}"),
                              subtitle: Text(l.detail),
                              trailing: Text(l.time, style: TextStyle(color: Colors.grey[500], fontSize: 12)),
                            );
                          })),
                  _negs.isEmpty
                      ? const Center(child: Text("暂无协商记录"))
                      : RefreshIndicator(onRefresh: _load, child: ListView.separated(
                          itemCount: _negs.length,
                          separatorBuilder: (_, _) => const Divider(height: 1),
                          itemBuilder: (_, i) {
                            final n = _negs[i];
                            return ListTile(
                              leading: const CircleAvatar(child: Icon(Icons.handshake, size: 18)),
                              title: Text("协商 ${n.id}"),
                              subtitle: Text("状态：${n.state}"),
                              trailing: Text("${n.round} 轮",
                                  style: TextStyle(color: Colors.grey[500], fontSize: 12)),
                            );
                          })),
                ]),
    );
  }
}
