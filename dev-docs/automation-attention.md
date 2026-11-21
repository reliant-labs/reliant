# Workflow attention policy

Attention is separate from execution lifecycle, semantic job outcome and trigger
health. A failed job stays failed, keeps its diagnostics, and emits its existing
failure event. A recovery workflow can take responsibility for a specific incident
without pretending the original job succeeded.

```yaml
attention:
  key: "repo:{{ inputs.repo }}:pr:{{ inputs.pr }}"
  on_start: recovering
  recovery_timeout: 30m
  on_success: resolved
  on_failure: action_required
```

## Contract

Only root unattended launch runs apply attention; interactive turns and nested
workflows do not mutate incidents. Omitting `attention` preserves legacy behavior.

- `key` is required: a literal or template over finalized `inputs` only. It is
  evaluated exactly once before graph execution and must produce a nonblank
  string no larger than 512 bytes. No workflow-name or PR-name inference occurs.
  Invalid dynamic keys fail visibly with existing diagnostics and legacy inbox
  attention as a fail-safe.
- `on_start` is absent, `recovering`, or `action_required`.
- `recovery_timeout` is required only for `on_start: recovering`, and forbidden
  otherwise: a Go duration from `1s` through `24h`, inclusive.
- `on_success` is absent, `resolved`, or `action_required`. There is no implicit
  resolution when an observer succeeds.
- `on_failure` is absent (defaults to `action_required`) or `action_required`.
  Cancellation, crashes and unsuccessful outcomes never resolve an incident.

Unknown fields, duplicate fields, non-string values, unsupported states and
invalid timeout combinations are errors. Direct protobuf definitions receive the
same policy validation. YAML round trips preserve absence and the raw template.

An observation workflow can declare only `attention: {key: '...'}`. Failure opens
attention; a same-key success does not clear it. A recovery workflow explicitly
opts into `recovering` and `resolved`. A recovery finishing without successful
explicit resolution escalates immediately. A configured repair trigger alone is
not recovery ownership: an actual run must register.

## Identity, deadlines and dismissals

Incidents correlate only within `(owner, project, key)`. Tenancy is derived from
stored chat/project ownership, never authored in YAML. Unrelated successes cannot
resolve an incident; older actors and replayed writes cannot erase newer state or
reopen a resolved episode.

A recovering lease is fixed from immutable run start, bounded by 24 hours, and
never extended by retries or history-compaction continue-as-new. Continuations
retain the original actor, evaluated key and deadline; a person's later reply
does not inherit recovery ownership. Every inbox read projects an expired lease as
`action_required`; this durable deadline does not rely on a running owner or a
reconciler. Expiry escalation appears in the inbox, without a push notification
in this first implementation. An expired actor cannot later suppress or resolve
the incident; a newly started recovery actor can claim it.

An open episode retains its dismissal identity during recovery, escalation and
expiry. A fresh incident after resolution receives a new episode, so dismissing
an old incident cannot hide a new one. Trigger diagnostic health is unchanged.
The additive inbox payload carries `incident_key`, `attention_state`,
`failure_class` (`unsuccessful`, `execution_error`, `recovery_expired`), and `detail`.
Older consumers can ignore these fields. An unfinished observer start alone never
suppresses legacy failures: suppression requires a completed managed actor or an
explicit recovering/action-required start with a matching open incident.

## Adoption

Pin the first Reliant release supporting this contract before enabling this YAML
in downstream workflows. Do not enable it on a server that only understands the
legacy schema. This change does not modify or enable control-plane workflows.

This initial primitive declares terminal transitions per workflow, not per node.
A repair workflow that finishes successfully as orchestration but exhausts its
repair attempts uses `on_success: action_required`, not `resolved`; separate
resolution into another correlated workflow if successful terminal branches
need different attention meanings.

The SDK producer landed in PR #2715 at main commit
`5d22b0058fbf95480d4b329e1af2584d27b3fefe`, following `sdk/go/README.md`.
The application consumer pins the published module
`v0.0.0-20261113190648-5d22b0058fbf` and is verified with `GOWORK=off`:
no local workspace, SDK replacement, or temporary module-graph override.
Earlier combined-tree checks used a temporary workspace while waiting for the
producer; those checks are not the final published-pin proof.
