# Bookkeeping as regular activities

Frozen on 2026-10-30, before the run's bookkeeping moved to local activities.
In these histories `ActivityLoadWorkflow`, `LoadPresetParams` and
`WorkflowCheckpoint` are regular activities (`ActivityTaskScheduled` /
`Started` / `Completed` events, each completion a workflow task of its own).
`bookkeeping-local-activities` (`bookkeepingLocalGate`,
`runtime/bookkeeping_local.go`) answers `DefaultVersion` for them, so every
call site keeps scheduling a regular activity for the whole run; new runs
record version 1 and run them as local activities (one `LocalActivity`
marker each). SHA256SUMS pins the original bytes. Never regenerate this
directory.
