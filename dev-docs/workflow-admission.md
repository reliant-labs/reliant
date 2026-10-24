# Workflow admission boundaries

Runnable workflow content is parsed strictly and statically validated by the
process handling the request. Parseable invalid drafts remain saveable for
iteration; they are not automation targets. Unknown YAML keys are parse errors,
not draft findings that can be ignored. Complete saves and managed project-file
writes reject invalid candidates before persistence or daemon write commands.

Automation enable and single-trigger activation use
`launch.ResolveValidatedRunWorkflow`, sharing launch's static tree gate.
Validation checks the referenced definitions themselves, not only the parent's
contracts against their input/output signatures. Per-pass definition caching
bounds cyclic/shared references; it is not an approved-revision store.

Declared-trigger resolution uses the same gate. A synced external file may
exist and be indexed even when invalid, but the activation reports BROKEN and
manual fire/resume rejects it before starting the fire backend. The worker's
scheduled/event fire paths also resolve the current declaration. Restoring a
valid definition recovers computed health without clearing a stored verdict.
Child workflow lookup outages remain lookup failures rather than permanent
invalid-definition verdicts. The builder's explicit draft-root test exception
is not granted to automation.

## What this does not guarantee

This gate uses the **current process's** compiled parser and validator. It does
not establish that a different Temporal worker supports the same language.
A new CLI passing scenarios, a desired deployment image, Kubernetes readiness,
and the stable `reliant-worker` BuildID are not parser capability evidence.

Investigation found no existing per-process capability heartbeat registry that
establishes the complete eligible Temporal poller set. Safely admitting new
syntax into a mixed fleet requires a separate protocol/routing design: publish
immutable process release/parser capabilities, expire evidence, account for
unknown/legacy pollers, and either require every eligible poller to support the
required contract or route tasks exclusively to compatible workers. Registration
without enforced eligibility cannot stop an unregistered old poller consuming a
shared queue. No new registry/table or desired-version approximation is added
here; cross-worker compatibility is still an open platform requirement.

Live refs are not immutable activation snapshots. Temporal history preserves a
completed load activity's result, while newly executing loads can resolve newer
synced content. This patch does not promise atomicity between an external file
sync and a later worker load, nor silently fall back to stale trading logic.

Task: `43632743-3c4e-4ade-90c8-336e314dc07b`. Runtime release recovery is independent
of this admission hardening; no activation or deployment is changed by it.
