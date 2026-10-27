// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"context"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// SignalName is a metadata-only doorbell. Database inbox remains authoritative.
const SignalName = "pr_review_wait"

// ContinuityReader adapts the single shared proof implementation.
type ContinuityReader struct{ Reader runcontinuity.Reader }

// Resolve proves a logical author's current exact recipient.
func (r ContinuityReader) Resolve(ctx context.Context, e runcontinuity.Execution) (runcontinuity.Result, error) {
	return runcontinuity.Resolve(ctx, r.Reader, e)
}

// TemporalSignaler sends to EXACT WorkflowID AND RunID; no latest-run fallback.
type TemporalSignaler struct {
	Client interface {
		SignalWorkflow(context.Context, string, string, string, interface{}) error
	}
}

// SignalReview rings only the enrolled subscription doorbell, not chat input.
func (s TemporalSignaler) SignalReview(ctx context.Context, e runcontinuity.Execution, subscriptionID string) error {
	if s.Client == nil || e.WorkflowID == "" || e.RunID == "" || subscriptionID == "" {
		return ErrUnavailable
	}
	return s.Client.SignalWorkflow(ctx, e.WorkflowID, e.RunID, SignalName, subscriptionID)
}
