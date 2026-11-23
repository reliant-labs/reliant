// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/org_provider_account"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/providerbackoff"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
)

// OrgPolicyWatcher wakes the chats an org policy refusal paused once the
// org's policy changes (orgpolicy.Watcher): a cap raised, a model allowed.
// It runs on every worker, the process whose activities meet the refusals.
type OrgPolicyWatcher struct{ w *orgpolicy.Watcher }

// OrgPolicyWatcherDeps is the policy resolver, and the repository and Temporal
// client a capacity wake needs.
type OrgPolicyWatcherDeps struct {
	Policies *orgpolicy.Resolver
	DB       *db.Repo
	Backoff  providerbackoff.Service
	Temporal *TemporalClient
}

// NewOrgPolicyWatcher is the watcher, installed as the process's so a refusal
// registers its org (orgpolicy.WatchRefused), or nothing (nil) with no
// policies (self-hosted) or nothing to wake.
//
//forge:root-only worker
func NewOrgPolicyWatcher(d OrgPolicyWatcherDeps) (*OrgPolicyWatcher, error) {
	if d.Policies == nil || d.DB == nil || d.Temporal == nil || d.Temporal.Client == nil {
		orgpolicy.SetProcessWatcher(nil)
		return nil, nil
	}
	w := orgpolicy.NewWatcher(d.Policies, orgChatWaker{targets: d.Backoff, temporal: d.Temporal})
	orgpolicy.SetProcessWatcher(w)
	return &OrgPolicyWatcher{w: w}, nil
}

// Run watches until ctx ends.
func (o *OrgPolicyWatcher) Run(ctx context.Context) error {
	o.w.Run(ctx)
	return nil
}

// orgChatWaker sends a capacity wake to an org's blocked chats.
type orgChatWaker struct {
	targets  capacitywake.OrgTargetLister
	temporal *TemporalClient
}

func (o orgChatWaker) WakeOrgChats(ctx context.Context, orgID, why string) ([]string, error) {
	return capacitywake.WakeOrg(ctx, o.targets, o.temporal.Client, orgID, why)
}

func (o orgChatWaker) WakeRuns(ctx context.Context, workflowIDs []string, why string) []string {
	return capacitywake.WakeRuns(ctx, o.temporal.Client, workflowIDs, why)
}

// orgAccountChatWaker is the org's chat waker for an org account change, nil
// without Temporal (a nil *TemporalClient in one would be a non-nil waker).
func orgAccountChatWaker(targets capacitywake.OrgTargetLister, temporal *TemporalClient) org_provider_account.OrgChatWaker {
	if targets == nil || temporal == nil || temporal.Client == nil {
		return nil
	}
	return orgChatWaker{targets: targets, temporal: temporal}
}
