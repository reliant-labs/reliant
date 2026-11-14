// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// CredentialMarker takes an account whose credential the provider rejected out
// of rotation (needs-sign-in), until its owner reconnects it. owner is the
// account's (accountOwner): for an org's key the mark is on the org's row, so it
// leaves every member's candidates at once.
type CredentialMarker interface {
	MarkNeedsSignIn(ctx context.Context, owner core.AccountOwner, accountID, reason string) error
}

// credentialRejection handles a provider refusing a routed account's own API
// key (401/403). The key is dead for every thread, so the account is marked
// needs-sign-in and the turn moves on: to another account of the provider
// (retry now; the router no longer offers the marked one), else to the chat's
// opted-in fallback model. It returns nil when err is not such a refusal or
// nothing can serve instead; the caller then fails or waits as it always did,
// and resolution reports the account as needing sign-in.
//
// Only API-key accounts are judged here. An OAuth session (it has a refresh
// token) is recovered or retired by the token refresh coordinator, and the
// managed Reliant key by its healer: a 401 on either is not proof the user's
// credential is gone.
func (r *AccountRouting) credentialRejection(ctx context.Context, err error, uc usageLimitContext) error {
	if !r.active() || r.Credentials == nil || uc.Account == nil || uc.Account.Config.AccountID == "" {
		return nil
	}
	cfg := uc.Account.Config
	if cfg.DriverID == "reliant" && cfg.AttributedOrgID != "" && drivers.CredentialRejected(err) {
		// The managed key minted in an org (P10): marked needs-sign-in, the
		// next resolution mints it again (drivers.EnsureOrgReliantKey) and the
		// org-less key serves until then. The org-less key is its healer's.
		if merr := r.Credentials.MarkNeedsSignIn(ctx, core.UserAccountOwner(uc.Scope.UserID), cfg.AccountID,
			"the gateway rejected the org's managed Reliant key; it is minted again"); merr != nil {
			logging.WarnContext(ctx, "[Failover] Could not mark a rejected org managed key for re-minting",
				"userID", uc.Scope.UserID, "accountID", cfg.AccountID, "error", merr)
		}
		return nil
	}
	if cfg.RefreshToken != "" || cfg.DriverID == "reliant" || cfg.DriverID == "local" || !drivers.CredentialRejected(err) {
		return nil
	}
	reason := "the provider rejected the saved API key: " + err.Error()
	if merr := r.Credentials.MarkNeedsSignIn(ctx, accountOwner(uc.Scope.UserID, cfg.DriverConfig), cfg.AccountID, reason); merr != nil {
		logging.WarnContext(ctx, "[Failover] Could not take a rejected API key out of rotation",
			"userID", uc.Scope.UserID, "provider", string(uc.Driver), "accountID", cfg.AccountID, "error", merr)
		return nil
	}
	logging.WarnContext(ctx, "[Failover] The provider rejected an account's API key; account needs sign-in",
		"userID", uc.Scope.UserID, "provider", string(uc.Driver), "accountID", cfg.AccountID)

	facts := types.UsageLimitFacts{Provider: string(uc.Driver), AccountID: cfg.AccountID, AccountLabel: accountName(cfg)}
	// exclude marks the account unusable even though uc.Account.Accounts still lists it.
	usable, _, aerr := r.Router.Alternatives(ctx, accountrouter.Request{
		UserID: uc.Scope.UserID, ThreadID: uc.ThreadID, Driver: uc.Driver,
		ModelID: uc.ModelID, Accounts: uc.Account.Accounts,
	}, cfg.AccountID)
	switch {
	case aerr == nil && usable:
		facts.AlternativeAccount = true
	default:
		facts.FallbackModel = r.fallbackModel(ctx, uc)
	}
	if !facts.AlternativeAccount && facts.FallbackModel == "" {
		return nil
	}
	return &types.UsageLimitFailure{
		Err: &ratelimit.UsageLimitError{
			Provider: models.ProviderDisplayName(string(uc.Driver)), StatusCode: 401,
			Err: fmt.Errorf("the saved API key was rejected: %w", err),
		},
		Facts: facts,
	}
}
