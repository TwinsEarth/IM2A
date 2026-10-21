// 模块职责：im2agent 客户端入口 —— 三态导航（审批 / 账本 / 审计），连自建后端
// 形态：Flutter（iOS / Android / Windows 三端同一份代码）
import 'package:flutter/material.dart';

import 'api_client.dart';
import 'screens/approvals_screen.dart';
import 'screens/audit_screen.dart';
import 'screens/ledger_screen.dart';

void main() => runApp(const Im2AgentApp());

class Im2AgentApp extends StatefulWidget {
  const Im2AgentApp({super.key});

  @override
  State<Im2AgentApp> createState() => _Im2AgentAppState();
}

class _Im2AgentAppState extends State<Im2AgentApp> {
  // 局域网可直接填 http://<电脑IP>:8080；生产填 https://<域名>
  final TextEditingController _baseUrl = TextEditingController(
    text: const String.fromEnvironment('BASE_URL', defaultValue: 'http://127.0.0.1:8080'),
  );
  final TextEditingController _token = TextEditingController(
    text: const String.fromEnvironment('ADMIN_TOKEN', defaultValue: ''),
  );
  ApiClient? _client;

  void _connect() {
    setState(() {
      _client = ApiClient(baseUrl: _baseUrl.text.trim(), token: _token.text.trim());
    });
  }

  @override
  Widget build(BuildContext context) {
    final client = _client;
    return MaterialApp(
      title: 'im2agent',
      theme: ThemeData(colorSchemeSeed: Colors.indigo, useMaterial3: true),
      home: client == null
          ? _ConnectScreen(baseUrl: _baseUrl, token: _token, onConnect: _connect)
          : _HomeScreen(client: client, onDisconnect: () => setState(() => _client = null)),
    );
  }
}

class _ConnectScreen extends StatelessWidget {
  const _ConnectScreen({required this.baseUrl, required this.token, required this.onConnect});

  final TextEditingController baseUrl;
  final TextEditingController token;
  final VoidCallback onConnect;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('连接 im2agent')),
      body: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            TextField(controller: baseUrl, decoration: const InputDecoration(labelText: '服务地址', hintText: 'http://192.168.1.10:8080')),
            const SizedBox(height: 12),
            TextField(controller: token, obscureText: true, decoration: const InputDecoration(labelText: 'ADMIN_TOKEN')),
            const SizedBox(height: 20),
            FilledButton(onPressed: onConnect, child: const Text('连接')),
            const SizedBox(height: 12),
            const Text('局域网：PROFILE=lan 启动后控制台会打印本机 IP。生产：https://域名（Caddy 自动 HTTPS）。',
                style: TextStyle(fontSize: 12, color: Colors.black54)),
          ],
        ),
      ),
    );
  }
}

class _HomeScreen extends StatefulWidget {
  const _HomeScreen({required this.client, required this.onDisconnect});

  final ApiClient client;
  final VoidCallback onDisconnect;

  @override
  State<_HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<_HomeScreen> {
  int _index = 0;

  @override
  Widget build(BuildContext context) {
    final pages = [
      ApprovalsScreen(client: widget.client),
      LedgerScreen(client: widget.client),
      AuditScreen(client: widget.client),
    ];
    return Scaffold(
      appBar: AppBar(
        title: const Text('im2agent · 运维'),
        actions: [IconButton(onPressed: widget.onDisconnect, icon: const Icon(Icons.logout), tooltip: '断开')],
      ),
      body: pages[_index],
      bottomNavigationBar: NavigationBar(
        selectedIndex: _index,
        onDestinationSelected: (value) => setState(() => _index = value),
        destinations: const [
          NavigationDestination(icon: Icon(Icons.fact_check_outlined), label: '审批'),
          NavigationDestination(icon: Icon(Icons.receipt_long_outlined), label: '账本'),
          NavigationDestination(icon: Icon(Icons.shield_outlined), label: '审计'),
        ],
      ),
    );
  }
}
