// Copyright (c) 2025 Reliant Labs

// Package gaterun runs one merge-queue batch's gate on a daemon machine of the
// repository's gate pool (pipeline build plan tasks E9b and E9f; MVP task M9).
//
// Two halves talk over the daemon command path:
//
//   - The server half, the Dispatcher (dispatch.go), is what the queue's
//     scheduler calls. It picks a connected member of the pool named by the
//     repository's `gate.pool` (machines labelled `pool=<name>`, default
//     `pool=gate`), asks that daemon to start the gate (CommandStart), and
//     polls it (CommandStatus) until the gate reports a [GateResult]. Start
//     and status are idempotent by gate id, so a dropped connection mid-gate
//     costs a poll, not the gate.
//   - The daemon half, [Jobs] over a [Runner], runs the gate's three steps
//     (pipeline redesign §3.3, "Gate isolation"). Only the middle one runs
//     pull-request code:
//     1. fetch, outside the boundary and credentialed: `reliant-queue/<batch>`
//     at the exact candidate sha into a fresh per-gate directory. The
//     credential is per invocation; none is written into the directory.
//     2. gate, inside a [Backend]: the stage script (script.go) runs lint →
//     test → build → sbom_scan from the repository's resolved stages,
//     stopping at the first stage that does not pass, and writes
//     gate-result.json.
//     3. report, outside: the output directory is read back through
//     Backend.CopyOut as a tar stream, as data only (report.go): size-capped,
//     never extracted, links refused, nothing executed. Then Teardown.
//
// Isolation fails closed. A machine whose isolated backend (sysbox, later vm)
// does not pass Detect and Preflight refuses the gate with
// [ErrNoIsolatedGateMachine]. The one exception is the `runc` backend
// (package runcgate), which is not isolated: it is chosen only for a shadow
// gate of a repository whose mode is shadow, and a result it produced is
// never [GateResult.Landable].
//
// Nothing sends a gate command yet; the queue's scheduler (M7) will, through
// the Dispatcher.
//
//forge:not-a-component: the merge queue's scheduler (M7) builds the Dispatcher over the worker's daemon router, the machine-pool store and pipeline_repos; the daemon builds Jobs in its gate command handler. Neither is wired by the composition root yet
package gaterun
