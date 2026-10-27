// Package reviewpublisheradapter implements private, explicitly constructed
// review authority adapters. Nothing registers tools, activates workflows or
// writes checks without the caller wiring these server-only services.
//
//forge:not-a-component: private server adapters constructed only by opt-in harness
package reviewpublisheradapter

import (
	"context"
	"encoding/json"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
)

// AuthorityStore is the SQL authority consumed by the adapters.
type AuthorityStore interface {
	LoadReviewAuthority(context.Context, string) (store.ReviewAuthority, error)
	RecordReviewEvidence(context.Context, store.ReviewEvidence) error
	PrepareIntent(context.Context, reviewpublisher.Intent) (reviewpublisher.Receipt, error)
}

// GraphSnapshot is a closed immutable root/dependency snapshot. Keys are the
// ActivityLoadWorkflow workflow_name references; JSON contains the actual loaded
// validated graph, not a re-read of current mutable source. Dynamic/unpinned
// dependencies are refused because every observed load must match this map.
type GraphSnapshot struct {
	AdmissionKey       string                     `json:"admission_key"`
	RootRef            string                     `json:"root_ref"`
	WorkflowType       string                     `json:"workflow_type"`
	Graphs             map[string]json.RawMessage `json:"graphs"`
	YAML               map[string][]byte          `json:"yaml"`
	CompletionNode     string                     `json:"completion_node"`
	CompletionActivity string                     `json:"completion_activity"`
	OutputSchema       string                     `json:"output_schema"`
}

// CompletionActivityName is a reserved internal activity, never a model tool.
const CompletionActivityName = "ActivityPrivateReviewCompletion"

// CompletionSchema pins the structured terminal result contract in the policy.
const CompletionSchema = "review-verdict-v1"

// GraphResult is produced by the policy-approved graph, not free-form prose.
type GraphResult struct {
	Verdict reviewpublisher.Verdict `json:"verdict"`
}

// CompletionRequest carries only a reference; authority comes from activity.GetInfo
// and the exact scheduled history event, loaded snapshots and final graph result.
type CompletionRequest struct {
	AssignmentID string `json:"assignment_id"`
}

// CheckRequest is the nonsecret fixed-operation CP bridge contract.
type CheckRequest struct {
	OwnerID                                                             string
	RepositoryID                                                        int64
	Owner, Repo, HeadSHA, Name, ExternalID, RunURL, Summary, Conclusion string
	CheckID                                                             int64
}

// CheckReceipt is a confirmed nonsecret check reference, never an access token.
type CheckReceipt struct {
	ID  int64
	URL string
}

// CheckBridge talks only to the dedicated CP internal publish/reconcile RPCs.
// Reconcile absence/ambiguity is an error, never permission for another create.
type CheckBridge interface {
	Publish(context.Context, CheckRequest) (CheckReceipt, error)
	Reconcile(context.Context, CheckRequest) (CheckReceipt, error)
}
