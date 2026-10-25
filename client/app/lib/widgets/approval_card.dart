// v1.3.1: 审批卡片——通过/修改/驳回/超时升级。
import "package:flutter/material.dart";

class ApprovalTicket {
  final String id;
  final String title;
  final String summary;
  final String agentId;
  final String state;
  final DateTime deadline;
  final Map<String, dynamic> payload;

  ApprovalTicket({required this.id, required this.title, required this.summary,
    required this.agentId, required this.state, required this.deadline, required this.payload});

  Duration get remain => deadline.difference(DateTime.now());
  bool get isUrgent => remain.inMinutes < 30;
  bool get isTimeout => remain.isNegative;
}

class ApprovalCard extends StatelessWidget {
  final ApprovalTicket ticket;
  final VoidCallback onApprove;
  final VoidCallback onReject;
  final VoidCallback onModify;
  final VoidCallback? onEscalate;

  const ApprovalCard({super.key, required this.ticket, required this.onApprove,
    required this.onReject, required this.onModify, this.onEscalate});

  @override
  Widget build(BuildContext context) {
    final urgent = ticket.isUrgent;
    return Card(
      margin: const EdgeInsets.all(12),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            CircleAvatar(child: Text(ticket.agentId.substring(0, 1).toUpperCase())),
            const SizedBox(width: 12),
            Expanded(child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(ticket.title, style: const TextStyle(fontWeight: FontWeight.bold)),
              Text(ticket.agentId, style: TextStyle(color: Colors.grey[600], fontSize: 12)),
            ])),
            if (urgent) Container(
              padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
              decoration: BoxDecoration(color: Colors.red[100], borderRadius: BorderRadius.circular(4)),
              child: Text(ticket.isTimeout ? "已超时" : "${ticket.remain.inMinutes}分钟",
                style: const TextStyle(color: Colors.red, fontSize: 12)),
            ),
          ]),
          const SizedBox(height: 12),
          Text(ticket.summary),
          if (ticket.isTimeout && onEscalate != null) ...[
            SizedBox(
              width: double.infinity,
              child: OutlinedButton.icon(
                onPressed: onEscalate,
                icon: const Icon(Icons.notification_important, color: Colors.red),
                label: const Text("已超时，紧急升级（短信/电话）",
                    style: TextStyle(color: Colors.red)),
              ),
            ),
            const SizedBox(height: 8),
          ],
          const SizedBox(height: 12),
          Row(children: [
            Expanded(child: FilledButton.icon(
              onPressed: onApprove, icon: const Icon(Icons.check), label: const Text("通过"))),
            const SizedBox(width: 8),
            Expanded(child: OutlinedButton.icon(
              onPressed: onModify, icon: const Icon(Icons.edit), label: const Text("修改"))),
            const SizedBox(width: 8),
            Expanded(child: TextButton.icon(
              onPressed: onReject, icon: const Icon(Icons.close), label: const Text("驳回"),
              style: TextButton.styleFrom(foregroundColor: Colors.red))),
          ]),
        ]),
      ),
    );
  }
}
