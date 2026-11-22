package approval

import (
	"context"
	"encoding/json"
)

// Approvals is the surface an app's handlers and sweepers depend on. [Store]
// is the Postgres implementation and [MemoryStore] the in-memory one for
// tests; take the interface where you want to swap them.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
type Approvals interface {
	// Propose records a pending item; idempotent on (Scope, Kind, SubjectRef).
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	Propose(ctx context.Context, it Item) (Item, error)
	// Get returns one item, or a NotFound error.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	Get(ctx context.Context, id string) (Item, error)
	// List returns the items matching f, newest first.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	List(ctx context.Context, f Filter) (Page, error)
	// ExpireDue moves pending items past their deadline to expired.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	ExpireDue(ctx context.Context, scope string) ([]Item, error)
	// Cancel withdraws the pending items matching f.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	Cancel(ctx context.Context, f Filter, note string) (int, error)
	// Decide records a person's verdict and runs the follow-up for an approval.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	Decide(ctx context.Context, d Decision, fn FollowUp) (Item, error)
	// Redrive re-runs the follow-up for approved items not yet handed off.
	//
	// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
	// be removed in forge v2. Copy it into your project if you depend on it.
	Redrive(ctx context.Context, f Filter, fn FollowUp) ([]Item, error)
}

func jsonValid(b []byte) bool { return json.Valid(b) }
