// v1.3.5: Flutter Desktop (PC) 入口——宽屏三栏同屏（审批 / 巡视 / 会话）。
// 多端同步：三个页面均直连网关，审批/协商/消息以服务端权威状态 + SSE 实时推送，
// 手机/手表/PC 登录同一账号即实时一致，无需本地对账。
import "package:flutter/material.dart";
import "../screens/approval_screen.dart";
import "../screens/conversation_screen.dart";
import "../screens/overview_screen.dart";

void main() => runApp(const DesktopApp());

class DesktopApp extends StatelessWidget {
  const DesktopApp({super.key});
  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: "IM2A Desktop",
      debugShowCheckedModeBanner: false,
      theme: ThemeData(colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF2F5DF5)),
          useMaterial3: true),
      home: const DesktopShell(),
    );
  }
}

class DesktopShell extends StatelessWidget {
  const DesktopShell({super.key});
  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Column(children: [
        // 顶部状态条：多端实时同步
        Container(
          width: double.infinity,
          color: const Color(0xFF2F5DF5),
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
          child: const Row(children: [
            Icon(Icons.devices, color: Colors.white, size: 18),
            SizedBox(width: 8),
            Text("IM2A · 个人行政中枢",
                style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
            Spacer(),
            Text("手机 / 手表 / PC 多端实时同步（SSE）",
                style: TextStyle(color: Colors.white70, fontSize: 12)),
          ]),
        ),
        // 三栏同屏：审批态 | 巡视态 | 会话态
        Expanded(child: Row(children: [
          const Expanded(flex: 3, child: ApprovalScreen()),
          const VerticalDivider(width: 1),
          const Expanded(flex: 4, child: OverviewScreen()),
          const VerticalDivider(width: 1),
          const Expanded(flex: 4, child: ConversationScreen()),
        ])),
      ]),
    );
  }
}
