package approval

import (
	"context"
	"encoding/json"
)

// Approvals is the surface an app's handlers and sweepers depend on. [Store]
// is the Postgres implementation and [MemoryStore] the in-memory one for
// tests; take the interface where you want to swap them.
type Approvals interface {
	// Propose records a pending item; idempotent on (Scope, Kind, SubjectRef).
	Propose(ctx context.Context, it Item) (Item, error)
	// Get returns one item, or a NotFound error.
	Get(ctx context.Context, id string) (Item, error)
	// List returns the items matching f, newest first.
	List(ctx context.Context, f Filter) (Page, error)
	// ExpireDue moves pending items past their deadline to expired.
	ExpireDue(ctx context.Context, scope string) ([]Item, error)
	// Cancel withdraws the pending items matching f.
	Cancel(ctx context.Context, f Filter, note string) (int, error)
	// Decide records a person's verdict and runs the follow-up for an approval.
	Decide(ctx context.Context, d Decision, fn FollowUp) (Item, error)
	// Redrive re-runs the follow-up for approved items not yet handed off.
	Redrive(ctx context.Context, f Filter, fn FollowUp) ([]Item, error)
}

func jsonValid(b []byte) bool { return json.Valid(b) }
