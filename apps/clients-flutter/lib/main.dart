import 'package:flutter/material.dart';

import 'models/paid_message.dart';
import 'screens/approval_home.dart';
import 'screens/conversation.dart';
import 'screens/oversight_view.dart';
import 'sdk/api_client.dart';
import 'sdk/state_store.dart';

/// 通过 `--dart-define=API_BASE_URL=http://127.0.0.1:8787` 覆盖服务端地址。
const String _defaultBaseUrl = String.fromEnvironment(
  'API_BASE_URL',
  defaultValue: 'http://127.0.0.1:8787',
);

void main() {
  runApp(const AgentImApp());
}

/// 双 Tab = 双态：审批态（默认）/ 巡视态。
/// ⚠ 本机无 Flutter SDK，未编译验证。
class AgentImApp extends StatefulWidget {
  const AgentImApp({super.key});

  @override
  State<AgentImApp> createState() => _AgentImAppState();
}

class _AgentImAppState extends State<AgentImApp> {
  late final ApiClient _api;
  late final StateStore _store;

  @override
  void initState() {
    super.initState();
    _api = ApiClient(baseUri: Uri.parse(_defaultBaseUrl));
    _store = StateStore(api: _api);
    _store.startPolling();
  }

  @override
  void dispose() {
    _store.dispose();
    _api.close();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Agent IM',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        useMaterial3: true,
        colorSchemeSeed: const Color(0xFF2F6BFF),
      ),
      home: DefaultTabController(
        length: 2,
        child: Scaffold(
          appBar: AppBar(
            title: const Text('管家'),
            bottom: const TabBar(
              tabs: <Widget>[
                Tab(text: '审批态'),
                Tab(text: '巡视态'),
              ],
            ),
            actions: <Widget>[
              IconButton(
                tooltip: '最近来意',
                icon: const Icon(Icons.mail_outline),
                onPressed: () {
                  final NavigatorState navigator = Navigator.of(context);
                  final List<PaidMessage> incoming = _store.incomingMessages;
                  if (incoming.isEmpty) {
                    ScaffoldMessenger.of(context).showSnackBar(
                      const SnackBar(content: Text('没有待处理的陌生人来意')),
                    );
                    return;
                  }
                  navigator.push(
                    MaterialPageRoute<void>(
                      builder: (BuildContext routeContext) => ConversationScreen(
                        store: _store,
                        messageId: incoming.first.id,
                      ),
                    ),
                  );
                },
              ),
            ],
          ),
          body: TabBarView(
            children: <Widget>[
              ApprovalHome(store: _store),
              OversightView(store: _store),
            ],
          ),
        ),
      ),
    );
  }
}
