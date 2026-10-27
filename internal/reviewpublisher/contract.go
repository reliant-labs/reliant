// Package reviewpublisher authorizes server-attested review completion and prepares
// durable check-publication intent. It does not publish to GitHub, mint tokens,
// expose an agent action, or install runtime wiring. Callers and adapters are
// trusted server code, never workflow/model-supplied authority.
//
//forge:not-a-component: intentionally unwired core; later server completion wiring must supply trusted adapters
package reviewpublisher

import "context"

// Service prepares publication only from trusted, persisted review completion.
type Service interface {
	Prepare(ctx context.Context, in PrepareInput) (Receipt, error)
}

// Execution identifies the original root and its exact execution, not a chat or
// a later turn. Adapters must retain a stable identity across legitimate rollover.
type Execution struct {
	RootID string
	RunID  string
}

// Binding is the immutable subject and reviewer authority pinned at admission.
// Digest is the effective immutable workflow digest, including dependencies.
type Binding struct {
	RepositoryID   int64
	PullNumber     int64
	HeadSHA        string
	Reviewer       Execution
	WorkflowDigest string
	PolicyRevision int64
	Generation     int64
}

// PrepareInput is a completion notification, not an authorization grant or verdict.
// Binding must match the server-loaded assignment exactly.
type PrepareInput struct {
	AssignmentID string
	Binding      Binding
}

// Policy is owner/admin-provisioned configuration loaded by the trusted adapter.
// Missing configuration is disabled; push access alone cannot provision it.
type Policy struct {
	Enabled        bool
	RepositoryID   int64
	WorkflowDigest string
	Revision       int64
	CheckName      string
}

// Completion is the persisted structured result of the approved graph. A model's
// text or a run's completed status alone cannot populate these trusted facts.
type Completion struct {
	Final          bool
	GraphSucceeded bool
	Verdict        Verdict
}

// Verdict is the approved workflow's structured review outcome.
type Verdict string

const (
	// Approve authorizes a successful review check when the graph succeeded.
	Approve Verdict = "approve"
	// Changes requests changes and produces a failing review check.
	Changes Verdict = "changes"
	// Incomplete cannot approve and produces a failing review check.
	Incomplete Verdict = "incomplete"
)

// Review is a server snapshot from the existing policy/admission/handoff state.
// No new competing assignment registry is implemented by this package.
type Review struct {
	AssignmentID      string
	Policy            Policy
	Binding           Binding
	Author            Execution
	CurrentGeneration int64
	Superseded        bool
	Cancelled         bool
	Completion        Completion
}

// ReviewSource reads canonical state and structured completion. Implementations
// must authenticate server-side lineage and load by exact assignment, not accept
// authority from PR text, action arguments or public origin-ledger claims.
type ReviewSource interface {
	LoadReview(ctx context.Context, assignmentID string) (Review, error)
}

// Head is the provider's current canonical PR subject, read without stale caching.
type Head struct {
	RepositoryID int64
	PullNumber   int64
	SHA          string
}

// HeadSource resolves current authenticated GitHub facts by repository ID and PR.
// Closed/unavailable/missing subjects must return an error, never a guessed head.
type HeadSource interface {
	CurrentHead(ctx context.Context, repositoryID, pullNumber int64) (Head, error)
}

// Intent is a bounded, server-generated publication payload and its authorization
// preconditions. It is not an exported GitHub action or bearer capability.
type Intent struct {
	AssignmentID string
	Binding      Binding
	CheckName    string
	Conclusion   string
	Summary      string
	DetailsURL   string
}

// Receipt identifies a durably prepared intent. Coalesced means the same subject
// already has an equivalent canonical intent, not that GitHub was updated.
type Receipt struct {
	IntentID  string
	Coalesced bool
}

// IntentStore is the adapter seam to the eventual admission transaction, NOT a
// request to create a second journal. PrepareIntent must atomically recheck the
// enabled policy/revision/digest/name, exact assignment/binding, current generation,
// terminal graph result and absence of cancellation/supersession against Intent.
// It persists before returning. Same-head duplicate completions coalesce to the
// canonical intent; different payloads cannot replace it. Failed writes return an
// error and must not report success; retries return the same durable receipt.
// Concurrent replicas must coalesce atomically. No remote GitHub call occurs here.
// A later publisher must revalidate these facts AND the current provider head
// immediately before mutation. This seam does not promise atomic DB/GitHub fencing
// or explicit equal-head rerun/revocation semantics.
type IntentStore interface {
	PrepareIntent(ctx context.Context, in Intent) (Receipt, error)
}

// Deps are trusted server adapters. A zero Deps constructs a disabled service.
// Configured services need all adapters and a canonical HTTPS origin for run links.
type Deps struct {
	Reviews ReviewSource
	Heads   HeadSource
	Intents IntentStore
	// RunOrigin is server configuration, never a completion/model-supplied URL.
	RunOrigin string
}
