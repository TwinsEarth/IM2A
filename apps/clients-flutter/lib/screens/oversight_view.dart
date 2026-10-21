import 'package:flutter/material.dart';

import '../models/approval.dart';
import '../models/negotiation.dart';
import '../sdk/state_store.dart';

/// 巡视态：看各子 Agent 在工作流里的位置，而不是读聊天记录。
///
/// 数据来源：`GET /api/state` 的 sessions / approvals；
/// 钻取只到「调用链 / 记忆摘要 / 中间结果」三层（docs/07 §2.2）。
class OversightView extends StatelessWidget {
  const OversightView({super.key, required this.store});

  final StateStore store;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: store,
      builder: (BuildContext context, Widget? child) {
        return RefreshIndicator(
          onRefresh: store.refresh,
          child: ListView(
            padding: const EdgeInsets.fromLTRB(12, 8, 12, 24),
            children: <Widget>[
              _summaryCard(context),
              if (store.pendingHumanSessions.isNotEmpty)
                _alertCard(context, store.pendingHumanSessions),
              _sectionTitle(context, '正在协商', store.activeSessions.length),
              ..._sessionTiles(context),
              _sectionTitle(context, '执行中', store.runningTickets.length),
              ..._runningTiles(context),
            ],
          ),
        );
      },
    );
  }

  Widget _summaryCard(BuildContext context) {
    final String synced =
        store.lastSyncAt == null ? '—' : _hourMinute(store.lastSyncAt!);
    final List<Widget> items = <Widget>[
      _Metric(label: '进行中协商', value: '${store.activeSessions.length}'),
      _Metric(label: '待我介入', value: '${store.pendingHumanSessions.length}'),
      _Metric(label: '执行中', value: '${store.runningTickets.length}'),
      _Metric(label: '最近同步', value: synced),
    ];
    return Card(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 12),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceAround,
          children: items,
        ),
      ),
    );
  }

  Widget _alertCard(BuildContext context, List<NegotiationSession> sessions) {
    return Container(
      margin: const EdgeInsets.only(top: 8),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.red.shade50,
        borderRadius: BorderRadius.circular(10),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Text(
            '有 ${sessions.length} 个协商需要你补充信息或授权',
            style: TextStyle(
              fontWeight: FontWeight.bold,
              color: Colors.red.shade900,
            ),
          ),
          const SizedBox(height: 4),
          const Text('协商已经暂停，不会继续消耗推理成本。'),
        ],
      ),
    );
  }

  Widget _sectionTitle(BuildContext context, String title, int count) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(4, 16, 4, 6),
      child: Text(
        '$title · $count',
        style: Theme.of(context).textTheme.labelLarge,
      ),
    );
  }

  List<Widget> _sessionTiles(BuildContext context) {
    final List<NegotiationSession> sessions = store.activeSessions;
    if (sessions.isEmpty) {
      return <Widget>[
        const ListTile(
          dense: true,
          leading: Icon(Icons.hourglass_empty),
          title: Text('当前没有进行中的协商'),
        ),
      ];
    }
    return sessions
        .map(
          (NegotiationSession session) => ListTile(
            leading: Icon(_iconFor(session.state)),
            title: Text(session.topic),
            subtitle: Text(
              '${agentDisplayName(session.initiatorAgentId)} ↔ '
              '${agentDisplayName(session.responderAgentId)} · '
              '第 ${session.round} 轮 · ${session.tokens} tokens',
            ),
            trailing: Text(session.state.label),
            onTap: () => _openSession(context, session),
          ),
        )
        .toList(growable: false);
  }

  List<Widget> _runningTiles(BuildContext context) {
    final List<ApprovalTicket> tickets = store.runningTickets;
    if (tickets.isEmpty) {
      return <Widget>[
        const ListTile(
          dense: true,
          leading: Icon(Icons.done_all),
          title: Text('当前没有执行中的任务'),
        ),
      ];
    }
    return tickets
        .map(
          (ApprovalTicket ticket) => ListTile(
            leading: const Icon(Icons.play_circle_outline),
            title: Text(ticket.digest),
            subtitle: Text('${ticket.agentLabel} · ${ticket.id}'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => _openTrace(context, ticket),
          ),
        )
        .toList(growable: false);
  }

  IconData _iconFor(TaskState state) {
    if (state.needsHuman) return Icons.pause_circle_outline;
    if (state == TaskState.working) return Icons.sync;
    return Icons.radio_button_checked;
  }

  void _openSession(BuildContext context, NegotiationSession session) {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      builder: (BuildContext sheetContext) => _SessionSheet(session: session),
    );
  }

  void _openTrace(BuildContext context, ApprovalTicket ticket) {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      builder: (BuildContext sheetContext) => _TraceSheet(ticket: ticket),
    );
  }
}

String _hourMinute(DateTime at) {
  final String h = at.hour.toString().padLeft(2, '0');
  final String m = at.minute.toString().padLeft(2, '0');
  return '$h:$m';
}

class _Metric extends StatelessWidget {
  const _Metric({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    return Column(
      children: <Widget>[
        Text(
          value,
          style: Theme.of(context).textTheme.titleLarge,
        ),
        const SizedBox(height: 2),
        Text(label, style: Theme.of(context).textTheme.labelSmall),
      ],
    );
  }
}

/// 协商摘要：总管只拿得到 digest，拿不到子 Agent 的原始记忆。
class _SessionSheet extends StatelessWidget {
  const _SessionSheet({required this.session});

  final NegotiationSession session;

  @override
  Widget build(BuildContext context) {
    final NegotiationArtifact? artifact = session.artifact;
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 16),
        child: ListView(
          shrinkWrap: true,
          children: <Widget>[
            Text(session.topic, style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 4),
            Text('状态：${session.state.label} · 第 ${session.round} 轮 · '
                '${session.tokens} tokens'),
            if (session.terminalReason != null)
              Text('终止原因：${session.terminalReason}'),
            const Divider(height: 24),
            Text('协商轨迹', style: Theme.of(context).textTheme.labelLarge),
            const SizedBox(height: 6),
            if (session.turns.isEmpty)
              const Text('尚无可见轮次（中间过程不产生用户通知）')
            else
              for (final NegotiationTurn turn in session.turns)
                ListTile(
                  dense: true,
                  contentPadding: EdgeInsets.zero,
                  leading: Text(turn.intent.label),
                  title: Text(turn.summary),
                  subtitle: Text(
                    '${agentDisplayName(turn.fromAgentId)} · ${turn.tokens} tokens',
                  ),
                ),
            if (artifact != null) ...<Widget>[
              const Divider(height: 24),
              Text('协商结论（${artifact.type}）',
                  style: Theme.of(context).textTheme.labelLarge),
              const SizedBox(height: 4),
              Text(artifact.summary),
            ],
            const SizedBox(height: 16),
            const Text(
              '说明：总管只能看到协调摘要（contentExposed: false），'
              '子 Agent 的原始记忆按契约隔离。',
              style: TextStyle(fontSize: 12),
            ),
          ],
        ),
      ),
    );
  }
}

/// 执行轨迹钻取：调用链 / 记忆读取 / 中间结果。
class _TraceSheet extends StatelessWidget {
  const _TraceSheet({required this.ticket});

  final ApprovalTicket ticket;

  @override
  Widget build(BuildContext context) {
    final Object? rawCalls = ticket.payload['skillCalls'];
    final List<Map<String, dynamic>> calls = rawCalls is List
        ? rawCalls.whereType<Map>().map(_asMap).toList(growable: false)
        : const <Map<String, dynamic>>[];
    final Object? rawMemory = ticket.payload['memoryReads'];
    final List<Map<String, dynamic>> memory = rawMemory is List
        ? rawMemory.whereType<Map>().map(_asMap).toList(growable: false)
        : const <Map<String, dynamic>>[];

    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 16),
        child: ListView(
          shrinkWrap: true,
          children: <Widget>[
            Text(ticket.digest, style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 4),
            Text('${ticket.agentLabel} · ${ticket.id} · ${ticket.state.label}'),
            const Divider(height: 24),
            Text('调用链', style: Theme.of(context).textTheme.labelLarge),
            if (calls.isEmpty)
              const Text('暂无 Skill 调用记录')
            else
              for (final Map<String, dynamic> call in calls)
                ListTile(
                  dense: true,
                  contentPadding: EdgeInsets.zero,
                  leading: Text((call['decision'] ?? 'ALLOW').toString()),
                  title: Text(
                    (call['skillId'] ?? call['name'] ?? '未知 Skill').toString(),
                  ),
                  subtitle: Text((call['detail'] ?? '').toString()),
                ),
            const Divider(height: 24),
            Text('记忆读取（摘要）', style: Theme.of(context).textTheme.labelLarge),
            if (memory.isEmpty)
              const Text('本次未读取记忆')
            else
              for (final Map<String, dynamic> item in memory)
                ListTile(
                  dense: true,
                  contentPadding: EdgeInsets.zero,
                  leading: const Icon(Icons.memory, size: 18),
                  title: Text((item['layer'] ?? 'working').toString()),
                  subtitle: Text(
                    (item['summary'] ?? item['tags'] ?? '').toString(),
                  ),
                ),
            const SizedBox(height: 16),
            const Text(
              '说明：此处只展示链路与摘要；原始内容不出 Agent 边界（契约 §6）。',
              style: TextStyle(fontSize: 12),
            ),
          ],
        ),
      ),
    );
  }

  Map<String, dynamic> _asMap(Map<dynamic, dynamic> value) {
    return value.map((Object? key, Object? val) =>
        MapEntry<String, dynamic>('$key', val));
  }
}
