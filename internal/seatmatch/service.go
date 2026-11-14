// Copyright (c) 2025 Reliant Labs
package seatmatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds the matcher's collaborators.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store holds the accounts and the workspace bindings.
	Store Store
	// Members reads live membership (orgs.Resolver). Nil is a deployment with
	// no orgs: nothing is ever attributed.
	//
	// forge:optional-dep
	Members Members
	// Rollout gates attributing (orgs.provider_workspace_seats_enabled). Nil
	// or off, the default, attributes nothing; clearing is never gated.
	//
	// forge:optional-dep
	Rollout Rollout
}

type service struct {
	deps Deps
}

// New creates the matcher.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Store == nil {
		return nil, errors.New("seatmatch: Deps.Store is required")
	}
	return &service{deps: deps}, nil
}

// enabled is whether this deployment attributes seats.
func (s *service) enabled() bool {
	return s.deps.Rollout != nil && s.deps.Rollout.GetProviderWorkspaceSeatsEnabled()
}

// liveMember is whether userID is, definitely, a live member of orgID. An
// unknown answer (no orgs) is false; an outage is its error.
func (s *service) liveMember(ctx context.Context, userID, orgID string) (bool, error) {
	if s.deps.Members == nil {
		return false, nil
	}
	member, ok, err := s.deps.Members.IsMember(ctx, userID, orgID)
	if err != nil {
		return false, fmt.Errorf("read %s's membership of org %s: %w", userID, orgID, err)
	}
	return ok && member, nil
}

func (s *service) MatchAccount(ctx context.Context, userID, accountID string) (Outcome, error) {
	accounts, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return Unchanged, err
	}
	var a *core.ProviderAccount
	for i := range accounts {
		if accounts[i].ID == accountID {
			a = &accounts[i]
			break
		}
	}
	if a == nil {
		return Unchanged, nil // deleted since, or not theirs
	}
	var binding *core.OrgProviderWorkspace
	if ws := core.ProviderWorkspaceID(*a); ws != "" {
		if binding, err = s.deps.Store.ProviderWorkspaceBinding(ctx, a.Provider, ws); err != nil {
			return Unchanged, err
		}
	}

	switch a.AttributionSource {
	case core.AttributionWorkspace:
		// Still covered by its org's binding: a seat stays the org's even
		// when its holder left (§6), until an admin releases it.
		if binding != nil && binding.OrgID == a.AttributedOrgID {
			return Unchanged, nil
		}
		cleared, err := s.deps.Store.ClearWorkspaceSeat(ctx, a.AttributedOrgID, a.ID)
		if err != nil || !cleared {
			return Unchanged, err
		}
		s.log(ctx, "org seat cleared: no binding of its org covers it", a.AttributedOrgID, *a)
		return Cleared, nil
	case "":
		if binding == nil || !s.enabled() {
			return Unchanged, nil
		}
		return s.attribute(ctx, *binding, *a)
	default:
		// The managed key minted in an org, or a seat an admin approved:
		// not the matcher's.
		return Unchanged, nil
	}
}

// attribute makes a, a sign-in in w, w's org's seat when its holder is a live member of the org.
func (s *service) attribute(ctx context.Context, w core.OrgProviderWorkspace, a core.ProviderAccount) (Outcome, error) {
	member, err := s.liveMember(ctx, a.UserID, w.OrgID)
	if err != nil || !member {
		return Unchanged, err
	}
	done, err := s.deps.Store.AttributeWorkspaceSeat(ctx, w, a.ID)
	if err != nil || !done {
		return Unchanged, err
	}
	s.log(ctx, "org seat attributed from a bound workspace", w.OrgID, a)
	return Attributed, nil
}

func (s *service) MatchWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (int, error) {
	if !s.enabled() {
		return 0, nil
	}
	signIns, err := s.deps.Store.ListWorkspaceSignIns(ctx, w.Provider, w.ExternalOrgID)
	if err != nil {
		return 0, err
	}
	var (
		n        int
		firstErr error
	)
	for _, a := range signIns {
		if a.AttributedOrgID != "" {
			continue // this org's already, or another's (the managed key minted in it)
		}
		out, err := s.attribute(ctx, w, a)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if out == Attributed {
			n++
		}
	}
	return n, firstErr
}

func (s *service) log(ctx context.Context, msg, orgID string, a core.ProviderAccount) {
	if s.deps.Logger == nil {
		return
	}
	s.deps.Logger.InfoContext(ctx, msg, "org_id", orgID, "account_id", a.ID, "user_id", a.UserID, "provider", a.Provider)
}
