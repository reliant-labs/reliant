package deploystate

import (
	"context"
	"errors"
)

// ErrNotFound is returned by [Store.Get] when no record exists for a
// key. Match it with errors.Is.
//
// A sentinel rather than a (nil, nil) return because the callers that
// matter treat the two cases differently: "never deployed" is a normal
// starting condition, while a nil record handed to code expecting one is
// a panic three frames later.
var ErrNotFound = errors.New("deploystate: no record for this key")

// Store is the pluggable backend. Five methods, and the count is a
// constraint rather than a coincidence.
//
// # Every method must be honestly implementable by a directory of files
//
// That is the acceptance test this interface was designed against, and
// [Local] is the proof. It is not about supporting an offline mode as a
// courtesy; it is the thing that keeps forge's half of reconciliation
// from quietly becoming a client for a mandatory server.
//
// The pressure is real and it is specific. Each of these would be
// natural to add, and each is refused:
//
//   - ListDue(shard, count) — "which environments need a pass". A
//     directory cannot shard, and more importantly SHOULD NOT: forge's
//     loop reconciles ONE project, so it knows its environments from the
//     config it already loaded. Sharding exists only for a fleet, and a
//     fleet driver already has it (control-plane's reconcilestore does
//     exactly this, over Postgres, with a lease and a fence).
//   - Transaction / WithTx — multi-key atomicity. Files cannot, and the
//     only caller who would need it is one coordinating writers it does
//     not control.
//   - AcquireLease / Fence — mutual exclusion between writers. A single
//     project's loop has one writer. A fleet has many, and fences them
//     one layer up where the lease actually lives.
//
// # The one honest gap: Put is last-writer-wins
//
// There is no compare-and-swap, and files are the reason. A CAS token on
// this interface would be unimplementable over a directory without
// lockfiles whose staleness semantics are a worse problem than the one
// they solve — a crashed writer's lock is indistinguishable from a live
// one's, and a local tool that hangs waiting for a lock nobody holds is
// a bug report.
//
// This is stated rather than papered over because the alternative is a
// method the local backend satisfies by ignoring its own argument, which
// is how an interface starts lying. Concurrent writes to one project's
// state are outside forge's model (one loop, one project); a backend
// that genuinely has concurrent writers fences them where it holds the
// lease, which is the layer that can.
//
// # Policy is a read, every pass
//
// [Store.Policy] is separate from [Store.Get] and is meant to be called
// on each pass rather than cached. Opting out of convergence must be
// INSTANT and must not require a deploy: an engineer who sets an
// environment to pinned mid-incident needs the next pass to honour it,
// not the next release. A policy baked into an artifact, or read once at
// start-up, fails exactly when it is needed.
type Store interface {
	// Get returns the record for one target, or ErrNotFound.
	Get(ctx context.Context, key Key) (Record, error)

	// Put writes a record. Last-writer-wins; see the type comment.
	Put(ctx context.Context, rec Record) error

	// List returns every record for one environment, in a stable order
	// (provider then service) so that CLI output and diffs do not
	// reshuffle between runs for no reason.
	//
	// Scoped to ONE environment on purpose. An unscoped "list
	// everything" would be the first method a fleet backend implements
	// with a paginated cursor and an owner filter, and at that point the
	// local implementation is a toy stapled to a real one.
	List(ctx context.Context, env string) ([]Record, error)

	// Policy returns the reconcile policy for one environment. A missing
	// policy is NOT an error: it returns PolicyObserve, because the
	// absence of an opinion must mean "change nothing", never "converge".
	Policy(ctx context.Context, env string) (Policy, error)

	// SetPolicy records the reconcile policy for one environment.
	SetPolicy(ctx context.Context, env string, p Policy) error
}
