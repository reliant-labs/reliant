# Approval-backed activation management

Agents can load `create_trigger_activation`, `update_trigger_activation`,
`enable_trigger`, and `pause_trigger` in an interactive orchestrator-tier chat.
Creation names a workflow and one declared trigger, a project, an explicit
`machine_id` (daemon ID) or `no_machine`, typed `params`, and a connection.
Updates preserve omitted fields; a supplied params map replaces the map.
Enable and pause accept only `trigger_id`.

The worker's existing tool factory and TriggerService perform the work. A
preview activity uses the app's definition/setup/match/access validators and
Enable dialog reachability check. The workflow uses the existing
ApprovalCreate/signal/ApprovalResolve flow, not an agent-authored question.
The card displays a bounded safe projection: machine ID/name, acting user and
identity, activation references, executable node categories and catalog tool
privileges. Integration action nodes name the concrete catalog action and read/
mutation classification; invoked tools name the catalog tool. Semantic targets
(owner/repository/channel/environment/URL origin+path) and trigger schedule/event/
match scope are disclosed through a bounded identifier-only allowlist. Unknown,
runtime-dependent or suspicious scope explicitly reads dynamic/unbounded. URL
userinfo, query and fragments, arbitrary input values, seed messages, source
expressions, preset values and graph literals are withheld even without a schema
secret marker. Identifier rendering is not a universal arbitrary-secret detector;
never put credentials into machine/connection names or target identifiers.
The memory-only fingerprint covers the complete canonical workflow dependency
closure, resolved presets/effective inputs, global tool bindings, connection
configuration and previous activation revision. Capture uses the launch/runtime
resolvers and CEL evaluator, with 64 dependency/effective-context, 2 MiB digest
material, 32 inline-depth and 32 KiB display limits; cycles are memoized (invalid
reference cycles fail validation). Unresolvable runtime/event-dependent privilege
choices or MCP dependencies fail closed and require app review.

One turn's calls are previewed first, then every card they need is raised at
once (`activationApprovalBatchChangeID`) and the turn waits for all of the
decisions. Cards raised together are `APPROVAL_TYPE_TOOL_CALL` rows that share
an `approvals.batch_id`; the chat's approval card and the Inbox offer the batch
as one decision (`BatchApprove`/`BatchDeny`, which skip any card decided
elsewhere meanwhile) and keep each card decidable on its own. Each decision
still binds only its own call's fingerprint. A denied tool-call card is
answered by its run (the call's refusal), never by a denial message over the
whole turn. Concurrent changes in one turn do not refuse each other: a commit
that Postgres rolled back because a swallowed read inside it hit a
serialization failure is retried like any 40001.

The executor requires the runtime's call-scoped fingerprint. Verification and
app mutation run in one SERIALIZABLE transaction; scoped dependency row locks
serialize existing edits, predicate reads protect shadowing/insertion, and the
activation revision is checked under its row lock. External schedule/webhook/
automation-grant reconciliation runs only after commit. A commit failure never
escapes as an external grant. Post-commit reconciliation failure reports that
configuration committed and must be inspected before retrying. This is the
owner-approved **atomic revision model**, not a permanently frozen runtime:
subsequent workflow edits still change existing activations. Changed dependencies
while waiting for approval require a new card. Declining, timing out or
interrupting never grants authority.

Unattended runs are refused even if their tools explicitly name these tools;
no approval card is raised. There is **no delegation setting in this phase**.
The CLI's agent-shell refusal stays unchanged. A later delegation policy needs
an owner decision; this PR does not let triggered runs grant themselves standing
privileges.

Configuration mutations are recorded in `trigger_changes` in the same DB
transaction as the trigger row, with user, originating chat/thread/tool call,
and safe before/after projections (no arbitrary values or credential bytes).
The legacy `run_id` audit field carries the thread identity and is labeled
Thread in the UI. A migration redacts older development-head snapshots/cards.
GetTrigger exposes the
newest 100 entries in Configuration history, separately from firing history
and health. App and CLI changes are audited too; their chat/run fields are empty.

Roll out API and worker together. New tools become discoverable through
load_tool on the next turn of a run whose tool filter/tier permits them. The
workflow approval wait survives worker restarts through Temporal history and
the existing durable approval rows.
