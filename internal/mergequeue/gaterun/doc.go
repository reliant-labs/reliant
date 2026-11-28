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
//   - The daemon half, package gatehost, runs the gate's three steps
//     (pipeline redesign §3.3, "Gate isolation"): a credentialed fetch
//     outside the boundary, the stage script inside a backend, and a report
//     read back as data. Only the middle one runs pull-request code.
//
// This package is the server half and the wire both halves share: the
// commands, the request and result types, the verdict rules and the gate's
// environment. It is server-safe: it runs no git and holds no repository
// bytes, so a server role may import it (internal/architecture's
// TestServerRolesRunNoCode). Everything that touches a checkout is in
// gatehost, which only the daemon imports.
//
// Isolation fails closed. A machine whose isolated backend (sysbox, later vm)
// does not pass Detect and Preflight refuses the gate with
// [ErrNoIsolatedGateMachine]. The one exception is the `runc` backend
// (package runcgate), which is not isolated: it is chosen only for a shadow
// gate of a repository whose mode is shadow, and a result it produced is
// never [GateResult.Landable].
//
// [PoolMembers] is the pool member choice the Dispatcher makes, exported for
// the merge queue's other daemon jobs (internal/mergequeue/jobrun), which run
// on the same pool.
//
//forge:not-a-component: the merge queue's scheduler builds the Dispatcher over the worker's daemon router, the machine-pool store and pipeline_repos; the daemon builds gatehost.Jobs in its gate command handler. Neither is wired by the composition root yet
package gaterun
