// Package cli — the shared reconcile spine under `forge env up` and
// `forge env deploy`.
//
// Both commands bring an environment toward its declared end-state. WHICH
// entity kinds each acts on is structural, not a value threaded through the
// call:
//
//	forge env deploy — the cluster apply (in-cluster workloads, compose
//	                   deploy targets, the External dispatch) plus the
//	                   Firebase frontend publish.
//	forge env up     — that same cluster apply, plus the concurrent infra
//	                   pre-warm, plus the host-process and frontend
//	                   dev-server phases. It always runs the whole loop.
//
// `up` used to carry a `scope` value masking phases off per --cluster-only /
// --host-only. Those flags are gone: `--target <name>` scopes a run by naming
// the entities it acts on, which narrows the work inside each phase without
// forge deciding that a cluster-declared service is "really" a host process.
// Placement stays where it is declared — a service that should run on the
// host during dev says so with a `host` block in its KCL.
//
// What remains genuinely variable is:
//
//	lifecycle — what a run does once everything is started:
//	              * once      — reconcile and RETURN. `forge env deploy` is
//	                            always `once`; so is the non-TTY `forge env up`
//	                            (start host/frontend processes, persist
//	                            their PIDs, print the summary, return —
//	                            stop later via `forge env down`).
//	              * supervise — start the long-lived host/frontend
//	                            processes, HOLD on a signal channel, and
//	                            cascade-teardown on Ctrl-C. The interactive
//	                            `forge env up` lifecycle.
//	              * auto      — defer the once-vs-supervise choice to a
//	                            TTY check at runtime (resolveUpLifecycle).
//	                            `forge env up`'s default when neither --watch
//	                            nor --background is given.
//
// In this vocabulary:
//
//	forge env deploy <env> = lifecycle=once, opts=<from flags>
//	forge env up <env>     = lifecycle=auto, opts={skipFrontend: true}
//
// The surgical knobs (tag / prune / dry-run / context override /
// targets / skip-frontend) live on deployOptions — the cluster reconcile's
// option surface, shared by both commands. `up`'s cluster step passes its
// deployOptions through the SAME named entry point deploy uses
// (reconcileCluster), so there is no blank-`deployOptions{}` literal standing
// in for "deploy with no options."
package cli

// reconcileLifecycle is the post-start behaviour: return immediately (once)
// or hold + teardown on signal (supervise). `forge env up`'s "auto" default —
// supervise in a TTY, once otherwise — is not a third enum value; it is
// resolved to one of these two by resolveUpLifecycle at the call site before
// the host phase, so no un-resolved lifecycle ever flows downstream.
type reconcileLifecycle int

const (
	// lifecycleOnce reconciles and returns. `forge env deploy` always; the
	// non-TTY `forge env up` after persisting PIDs + printing the summary.
	lifecycleOnce reconcileLifecycle = iota
	// lifecycleSupervise holds the foreground on a signal channel and
	// cascade-tears-down the host/frontend processes on Ctrl-C. The
	// interactive `forge env up`.
	lifecycleSupervise
)

// resolveUpLifecycle is the pure TTY-aware lifecycle decision for
// `forge env up`. It is the LLM-first fix: an agent / CI invocation of
// `forge env up <env>` must NOT hang on the interactive Ctrl-C hold, so
// the DEFAULT (neither --watch nor --background) supervises only when a TTY
// is present and otherwise returns after detaching — the same end-state as
// --background.
//
// Precedence (explicit flags beat the TTY default):
//
//   - --background wins outright: the user explicitly asked to detach and
//     return, so lifecycle=once regardless of --watch or the TTY. (When
//     both --watch and --background are set, --background is the documented
//     winner — "detach and return" is the more conservative, non-blocking
//     outcome, and matches the flag's long-standing "return immediately"
//     contract.)
//   - --watch forces supervise (hold + Ctrl-C teardown) even without a
//     TTY, for a human who pipes `forge env up` output through a tool.
//   - Otherwise the TTY decides: a terminal → supervise (today's
//     interactive behaviour); no terminal → once (return after start).
//
// Split out from runUp so the decision is unit-tested by injecting isTTY
// and the two flags.
func resolveUpLifecycle(isTTY, watch, background bool) reconcileLifecycle {
	switch {
	case background:
		return lifecycleOnce
	case watch:
		return lifecycleSupervise
	case isTTY:
		return lifecycleSupervise
	default:
		return lifecycleOnce
	}
}
