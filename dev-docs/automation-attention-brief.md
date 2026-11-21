# Owner-approved automation attention work

Owner approved an additive workflow attention contract, independent of execution lifecycle and semantic job outcome. Requested behavior: correlate runs by incident/PR; record automation recovery ownership; escalate only when human action is needed; resolve upon that incident's recovery. Existing workflows without attention configuration retain behavior. Preserve failed outcomes, history, and workflow_event failure routing; do not disguise unsuccessful tasks as successful just to suppress inbox entries. No runtime-name special cases for PR pipeline.

Verified research:
- internal/workflow/runtime/workflow.go captures terminal outcome and completes execution with outcome=failure; workflow_status.go records semantic failed run_status and emits RunEventFailed. Keep this compatible.
- internal/workflow/runtime/activities/handlers/workflow_status.go includes failure node diagnostics, unread notifications, first-in-streak notification, launch-event status, run event emissions.
- internal/triggers/health.go derives trigger health; maintain diagnostics independently of human attention.
- internal/handlers/inbox/inbox.go builds one automation failing item per enabled trigger streak. Unrelated PR success currently clears another PR failure; separate triggers duplicate incidents; running repair doesn't suppress an existing item.
- internal/store/inbox_dismissal_repo_ext.go has inbox projection and stable dismissal episodes; internal/db/core/inbox.go types. Tenant/user scoping mandatory, no cross-project correlation unless explicitly safe.
- PR review changes-requested/skipped/superseded already are successful orchestration. pr-fix unsuccessful repair reporting is also successful orchestration, while actual preparation/GitHub errors can legitimately fail. slot_lost is a potential misclassified supersession terminal.

Implementation requirements:
- Additive validated YAML contract for attention incident correlation and explicit recovering/action-required/resolved transitions, independent of outcome.
- Durable, idempotent state updates with deterministic ordering/fencing: stale finishes cannot erase newer incidents; unrelated success cannot clear an incident; duplicate/replayed activities cannot reopen resolved episodes; suppression must never last forever after a dead recovery owner. Recovery failures/exhaustion need fail-safe escalation. Define/test identity boundaries, TTL/ownership semantics and episode dismissal behavior explicitly.
- Do not infer recovery from existence of a configured repair trigger (doesn't prove launched).
- Existing contracts keep defaults and wire behavior; generated changes only through source inputs and generators; migrations via task db:migration.
- Scope builds/tests only; no full gates, merge, release or deploy. Open non-draft PR(s), no queue labels. Control-plane adoption only after producer supported/pinned; don't enable unsupported YAML in live workflows.

Research must settle concrete syntax and transition semantics before parallel implementation; owner has approved the architecture above, not an unrelated notification rewrite. This file is a durable implementation briefing, not a claim implementation exists.
