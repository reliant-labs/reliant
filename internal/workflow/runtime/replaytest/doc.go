// Copyright (c) 2025 Reliant Labs

// Package replaytest pins replay compatibility of the DynamicWorkflow against
// recorded Temporal histories.
//
// Why this exists: Temporal replays a workflow's recorded event history
// through the CURRENT workflow code every time a worker picks up an in-flight
// run (worker restart, deploy, sticky-cache eviction). If the code's sequence
// of workflow commands (activities, timers, signals-handling, child workflows,
// side effects, ...) no longer matches the recorded history, the run is
// wedged forever with WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR
// (TMPRL1100) — exactly the production incident this suite was built after.
//
// The fixtures under fixtures/ are real histories captured from an ephemeral
// Temporal dev server running the production worker registration path
// (workersetup.StartWorker) with a scripted LLM. They are the contract:
// "current workflow code must stay replay-compatible with THESE histories."
//
// fixtures/frozen/<set>/ holds histories recorded before a command-sequence
// change; they are never regenerated and must keep replaying through the
// change's workflow.GetVersion gate. See fixtures/README.md for what each
// fixture pins, how to regenerate (make replay-fixtures) and how to freeze the
// old shape first (make freeze-replay-fixtures).
//
// Everything else in this package is a _test.go file: this doc is the only
// non-test source, which is also where forge reads package markers from.
//
//forge:exclude-contract: test-support: the suite and its fakes (the scripted LLM, the replay harness) live in _test.go files; nothing imports this package and there is no service to bind
package replaytest
