// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// AccountScope is the drivers.Scope a person's act on chat (a send, a pick, a
// launch they are behind) resolves models in: the chat's org and its
// accounts, as the run's own calls resolve them (llmCallSpec.scope), so what
// launch admits and what the run is served from are the same set under the
// same org policy (control-plane docs/design/org-policies.md §4.1, §4.2). A
// chat of no org, and no chat, is the person's own accounts.
func AccountScope(userID string, chat *db.Chat) drivers.Scope {
	if chat == nil || chat.OrgID == nil || *chat.OrgID == "" {
		return drivers.PersonalScope(userID)
	}
	return drivers.Scope{UserID: userID, OrgID: *chat.OrgID, Attended: true}
}

// launchAccountScope is AccountScope for a launch: unattended when nobody is
// behind it (spec.Unattended: a stored trigger fired), so the org's
// automations section applies as it will to the run's calls (§11.6). It draws
// on every source the owner has (UnattendedAnySource): whether an org_only
// automation's model has an org account is checkOrgAccountsOnly's question,
// asked next, with its own answer.
func launchAccountScope(spec Spec, userID string, chat *db.Chat) drivers.Scope {
	scope := AccountScope(userID, chat)
	if spec.Unattended && !scope.Personal() {
		scope.Attended, scope.UnattendedAnySource = false, true
	}
	return scope
}

// policyAllowsSubstitute reports whether a send moves selector, refused by
// the org's policy, onto a model and provider the policy allows: the same
// model on another provider, else its tier (drivers.SubstituteUnservableModel
// over the policy's narrowed set).
func (l *Launcher) policyAllowsSubstitute(ctx context.Context, accounts drivers.Scope, selector interface{}) bool {
	available, err := drivers.LookupAvailableDrivers(ctx, accounts)
	if err != nil {
		return false
	}
	_, ok := drivers.SubstituteUnservableModel(selector, available)
	return ok
}

// PolicyRefusedError reports a launch or a send the org's policy refused
// (control-plane docs/design/org-policies.md §4.1, §4.2, §5): a model input
// names a model, or one only a provider or kind of account serves, that the
// org's policy does not allow. Nothing was written. It is the policy's
// verdict, not a fault: the person picks another model, or an admin changes
// the policy. A trigger's firing records it failed and pauses the trigger,
// once (core.TriggerPausedOrgPolicy).
type PolicyRefusedError struct {
	// Input is the model input's path ("model", "<group>.model").
	Input string
	// Refusal is the policy's refusal: its reason and message.
	Refusal *orgpolicy.Refusal
}

func (e *PolicyRefusedError) Error() string {
	return "input '" + e.Input + "': " + e.Refusal.Message()
}

// Unwrap is a FailedPrecondition carrying the refusal's reason
// (provider_not_allowed, model_not_allowed) as x-forge-error-reason.
func (e *PolicyRefusedError) Unwrap() error {
	return svcerr.WithReason(svcerr.FailedPrecondition(e.Error()), e.Refusal.Reason)
}

// PolicyRefusal returns the org policy refusal a launch error is, if it is
// one.
func PolicyRefusal(err error) (*PolicyRefusedError, bool) {
	var refused *PolicyRefusedError
	if errors.As(err, &refused) && refused != nil && refused.Refusal != nil {
		return refused, true
	}
	return nil, false
}
