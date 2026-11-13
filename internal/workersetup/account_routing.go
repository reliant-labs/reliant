// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// accountRouterStore satisfies accountrouter.Store over the account repository.
// The router declares its own copies of the limit and assignment types (it is
// consumed, not coupled to the database), so this is only a field-for-field
// translation.
type accountRouterStore struct{ repo AccountAssignmentStore }

var _ accountrouter.Store = accountRouterStore{}

func (s accountRouterStore) GetThreadAssignment(ctx context.Context, threadID, provider string) (*accountrouter.Assignment, error) {
	a, err := s.repo.GetThreadAssignment(ctx, threadID, provider)
	if err != nil || a == nil {
		return nil, err
	}
	out := accountrouter.Assignment(*a)
	return &out, nil
}

func (s accountRouterStore) SetThreadAssignment(ctx context.Context, a accountrouter.Assignment, expectedAccountID string) (bool, error) {
	return s.repo.SetThreadAssignment(ctx, core.Assignment(a), expectedAccountID)
}

func (s accountRouterStore) ListAccountLimits(ctx context.Context, userID string) (map[string]accountrouter.LimitState, error) {
	limits, err := s.repo.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, err
	}
	return routerLimits(limits), nil
}

func (s accountRouterStore) ListOrgAccountLimits(ctx context.Context, orgID string) (map[string]accountrouter.LimitState, error) {
	limits, err := s.repo.ListOwnedAccountLimits(ctx, core.OrgAccountOwner(orgID))
	if err != nil {
		return nil, err
	}
	return routerLimits(limits), nil
}

// routerLimits translates the store's limit rows for the router.
func routerLimits(limits map[string]core.LimitState) map[string]accountrouter.LimitState {
	out := make(map[string]accountrouter.LimitState, len(limits))
	for id, l := range limits {
		ls := accountrouter.LimitState{
			AccountID: l.AccountID, UserID: l.UserID, Status: l.Status,
			RepresentativeWindow: l.RepresentativeWindow, Source: l.Source,
			ObservedAt: l.ObservedAt, LimitedUntil: l.LimitedUntil, LimitedWindow: l.LimitedWindow,
			LimitedProbes: l.LimitedProbes, UsageAPIError: l.UsageAPIError,
		}
		for _, w := range l.Windows {
			ls.Windows = append(ls.Windows, accountrouter.LimitWindow(w))
		}
		out[id] = ls
	}
	return out
}

func (s accountRouterStore) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return s.repo.RecentAssignmentCounts(ctx, accountIDs, since)
}

// accountRouting is the multi-account routing a worker's call_llm and compact
// activities share. It is inactive until a per-account driver builder is wired
// (see handlers.AccountRouting.DriverFor): accounts are never assigned that
// cannot then be used.
func accountRouting(repo AccountRoutingStore, recorder handlers.AccountLimitRecorder, notices ...orgKeyNoticeQueue) *handlers.AccountRouting {
	marker := credentialMarker{repo: repo}
	if len(notices) > 0 {
		marker.notices = notices[0]
	}
	store := accountRouterStore{repo: repo}
	return &handlers.AccountRouting{
		Router:                accountrouter.NewInstrumented(accountrouter.Deps{Store: store}),
		ClearOtherAssignments: repo.ClearOtherThreadAssignments,
		Store:                 store,
		Settings:              repo,
		DriverFor:             accountDriverFor,
		Recorder:              recorder,
		Credentials:           marker,
		Funding:               repo,
	}
}

// OrgAdminNotifier emails an org's owners and admins that a provider rejected
// one of the org's own API keys. controlplane.Service is one.
type OrgAdminNotifier interface {
	NotifyOrgKeyRejected(ctx context.Context, n controlplane.OrgKeyRejectedNotice) (deduplicated bool, err error)
}

// orgKeyNoticeQueue is where a rejected org key's notice is started: the
// Temporal client and the task queue its workflow runs on.
type orgKeyNoticeQueue struct {
	starter   noticeStarter
	taskQueue string
}

// credentialMarker retires an account whose API key the provider rejected, the
// way the token refresh coordinator retires one whose refresh token it did.
// For an org's own key it also has the org's owners and admins emailed, who
// alone can rotate it (org-provider-accounts §4.7; the inbox item is listed
// from the mark itself).
type credentialMarker struct {
	repo AccountCredentialStore
	// notices is unset without Temporal: the inbox item is the only notice.
	notices orgKeyNoticeQueue
}

// MarkNeedsSignIn marks the account on its owner's row: a person's own, or the
// org's for an org key.
func (m credentialMarker) MarkNeedsSignIn(ctx context.Context, owner core.AccountOwner, accountID, reason string) error {
	acct, err := m.repo.PatchOwnedProviderAccount(ctx, owner, accountID, core.AccountPatch{NeedsReauth: &reason})
	if err != nil {
		return err
	}
	if owner.IsOrg() && m.notices.starter != nil && acct.NeedsReauthAt != nil {
		m.notifyOrgAdmins(context.WithoutCancel(ctx), owner.ID, acct, reason)
	}
	return nil
}

// notifyOrgAdmins starts the notice of the rejected key (OrgKeyRejectedNoticeWorkflow).
// The incident is when the key was first marked: a mark keeps its time until
// the key is rotated, so every worker that meets the same rejection, and every
// retry, names the same incident and starts the same workflow. A start that
// fails is logged; the mark stands and the inbox lists it whatever happens here.
func (m credentialMarker) notifyOrgAdmins(ctx context.Context, orgID string, acct core.ProviderAccount, reason string) {
	n := controlplane.OrgKeyRejectedNotice{
		OrgID: orgID, IncidentKey: acct.NeedsReauthAt.UTC().Format(time.RFC3339Nano), AccountID: acct.ID,
		Provider: models.ProviderDisplayName(acct.Provider), Label: acct.Label, KeyHint: acct.KeyHint, Reason: reason,
	}
	if err := startOrgKeyNotice(ctx, m.notices.starter, m.notices.taskQueue, n); err != nil {
		logging.ErrorContext(ctx, "Could not start emailing the org's admins about a rejected key; the inbox still lists it",
			"orgID", orgID, "accountID", acct.ID, "error", err)
	}
}
