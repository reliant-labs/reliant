// Package deploystate is the pluggable backend for forge's reconcile
// state: what forge last shipped to a target, what it last OBSERVED
// running there, and the per-environment policy that says whether forge
// is allowed to do anything about the difference.
//
// # Why this is in pkg/ and not internal/
//
// pkg/ is forge's public import surface, and Go's internal/ rule is what
// makes that boundary the plug point: no other module may import forge's
// internal/ tree. An operator of many forge projects — Reliant, or anyone
// else — implements [Store] against an interface it can actually import,
// and forge's own loop is one caller among several. Put the interface in
// internal/ and there is no pluggable backend; there is only a fork.
//
// # The constraint that keeps this honest
//
// IF IT DOES NOT WORK AGAINST A DIRECTORY OF FILES, IT IS IN THE WRONG
// REPO. [Local] is not the fallback, it is the acceptance test. The
// moment a method needs a transaction, or a fleet-wide query like "every
// environment across every project due for a pass", the file
// implementation becomes a lie and what has actually shipped is a client
// for a mandatory server with a stub attached.
//
// So this interface has five methods and none of them is a scheduler.
// Fleet-scale concerns — sharding, leases, fences, placement — belong to
// whoever is DRIVING many instances of forge's per-project loop, and
// they are already solved there (control-plane's internal/reconcilestore
// holds the lease and the fence). Forge's loop only ever reconciles one
// project's environments, and one project's state is a directory.
//
// The one thing files genuinely cannot offer is compare-and-swap: [Put]
// is last-writer-wins. That is not a gap papered over, it is the
// boundary drawn correctly — a CAS token on this interface would exist
// solely to serve a fleet backend, and the fleet backend already fences
// its writes one layer up, where it holds the lease that makes a fence
// meaningful. See [Store] for the full argument.
//
// # What lives here
//
//   - [Record] — one target's desired and observed halves, side by side.
//   - [State] — five values, and [StateUnknown] is the zero value.
//   - [Policy] — observe / converge / pinned, defaulting to observe.
//   - [Store] — the interface.
//   - [Local] — files, under .forge/state. No server, no network.
//   - [Remote] — HTTP+JSON against an endpoint. Proof the seam is real.
package deploystate
