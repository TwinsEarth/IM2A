/// human-agent-im Flutter 客户端入口。
///
/// 主界面三态（§7.1）：审批态（默认） / 巡视态 / IM 会话。
/// 手表端为独立精简入口（审批卡片是/否/稍后 + 未读摘要 + 紧急联系人），见 Phase 1。
library;

import 'package:flutter/material.dart';

import 'screens/approval_screen.dart';
import 'screens/conversation_screen.dart';
import 'screens/devices_screen.dart';
import 'screens/overview_screen.dart';
import 'services/ble/ble_service.dart';

void main() {
  runApp(const HumanAgentImApp());
}

class HumanAgentImApp extends StatelessWidget {
  const HumanAgentImApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: '人机混合 IM',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF2F5DF5)),
        useMaterial3: true,
      ),
      home: const HomeShell(),
    );
  }
}

class HomeShell extends StatefulWidget {
  const HomeShell({super.key});

  @override
  State<HomeShell> createState() => _HomeShellState();
}

class _HomeShellState extends State<HomeShell> {
  int _index = 0; // 默认进入审批态（§7.1：设计重心是审批与巡视，不是聊天）
  final BleService _ble = BleService();

  @override
  Widget build(BuildContext context) {
    final pages = <Widget>[
      const ApprovalScreen(),
      const OverviewScreen(),
      const ConversationScreen(),
      DevicesScreen(ble: _ble),
    ];
    return Scaffold(
      body: IndexedStack(index: _index, children: pages),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _index,
        onDestinationSelected: (i) => setState(() => _index = i),
        destinations: const [
          NavigationDestination(
            icon: Icon(Icons.rule_outlined),
            selectedIcon: Icon(Icons.rule),
            label: '审批',
          ),
          NavigationDestination(
            icon: Icon(Icons.dashboard_outlined),
            selectedIcon: Icon(Icons.dashboard),
            label: '团队',
          ),
          NavigationDestination(
            icon: Icon(Icons.chat_bubble_outline),
            selectedIcon: Icon(Icons.chat_bubble),
            label: '会话',
          ),
          NavigationDestination(
            icon: Icon(Icons.watch_outlined),
            selectedIcon: Icon(Icons.watch),
            label: '设备',
          ),
        ],
      ),
    );
  }
}
