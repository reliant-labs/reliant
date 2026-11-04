// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
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
	return out, nil
}

func (s accountRouterStore) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return s.repo.RecentAssignmentCounts(ctx, accountIDs, since)
}

// accountRouting is the multi-account routing a worker's call_llm and compact
// activities share. It is inactive until a per-account driver builder is wired
// (see handlers.AccountRouting.DriverFor): accounts are never assigned that
// cannot then be used.
func accountRouting(repo AccountRoutingStore, recorder handlers.AccountLimitRecorder) *handlers.AccountRouting {
	store := accountRouterStore{repo: repo}
	return &handlers.AccountRouting{
		Router:                accountrouter.NewInstrumented(accountrouter.Deps{Store: store}),
		ClearOtherAssignments: repo.ClearOtherThreadAssignments,
		Store:                 store,
		Settings:              repo,
		DriverFor:             accountDriverFor,
		Recorder:              recorder,
		Credentials:           credentialMarker{repo: repo},
	}
}

// credentialMarker retires an account whose API key the provider rejected, the
// way the token refresh coordinator retires one whose refresh token it did.
type credentialMarker struct{ repo AccountCredentialStore }

func (m credentialMarker) MarkNeedsSignIn(ctx context.Context, userID, accountID, reason string) error {
	_, err := m.repo.PatchProviderAccount(ctx, userID, accountID, core.AccountPatch{NeedsReauth: &reason})
	return err
}
