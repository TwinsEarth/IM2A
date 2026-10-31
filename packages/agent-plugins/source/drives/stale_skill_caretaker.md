---
drive_id: stale_skill_caretaker
description: "scope == self_owned_skills AND lifecycle_state == stale AND patch_count == 0 AND ageDays >= 14"
priority: 0.7
trigger_hint: "curator_audit.lifecycle_state == stale"
scope: self_owned_skills
---

# Stale skill caretaker

A durable concern: notice self-owned skills that have been stale for at least
14 days without a patch, and propose a bounded improvement when the Harness has
an idle slot.

This declaration does not execute anything. The trigger hint is only a signal
for future scheduling.
