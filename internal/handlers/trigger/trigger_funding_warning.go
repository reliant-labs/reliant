// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// driverSources reads the accounts a scope may draw on:
// drivers.LookupAvailableDrivers in production; a test substitutes its own.
type driverSources func(ctx context.Context, scope drivers.Scope) (models.AvailableDrivers, error)

// fundingWarning is what a create, an update or an enable that turned t on
// tells its caller when t's runs may spend only its organization's accounts
// and none of them can run its model yet (org-provider-accounts §11.3). The
// write stands: accounts change, and until an admin adds one each fire fails
// with no_org_account (launch's checkOrgAccountsOnly, which this mirrors).
//
// The model is resolved as the fire will resolve it: through the one scoped
// loader, in the unattended scope of t's stamp, so the org's automations
// policy applies exactly as it will to the run. A paused trigger, one of no
// org, one that may use any source, one with no model input and one on a
// local model get no warning; neither does a failed read, which is logged:
// a warning is advice, never a guess.
func (s *Service) fundingWarning(ctx context.Context, t *core.Trigger) *reliantv1.TriggerFundingWarning {
	if t == nil || !t.Enabled || t.OrgID == nil || *t.OrgID == "" {
		return nil
	}
	scope := drivers.Scope{UserID: t.UserID, OrgID: *t.OrgID, UnattendedAnySource: !t.UnattendedFunding.OrgOnly()}
	if !scope.OrgFundedOnly() {
		return nil
	}
	lookup := s.sources
	if lookup == nil {
		lookup = drivers.LookupAvailableDrivers
	}
	model, unserved, err := launch.UnservedModel(ctx, lookup, scope, t.Params["model"])
	if err != nil {
		logging.WarnContext(ctx, "could not read the organization's AI accounts to check an automation's model",
			"trigger_id", t.ID, "org_id", *t.OrgID, "error", err)
		return nil
	}
	if !unserved {
		return nil
	}
	return &reliantv1.TriggerFundingWarning{
		OrgId:   *t.OrgID,
		Model:   model,
		Message: "No account of this organization can run " + model + " yet, so its runs will fail until an admin adds one.",
	}
}
