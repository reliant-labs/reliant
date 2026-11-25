package insights

import (
	"context"
	"errors"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// CostHiddenNote is added to a response whose cost figures were withheld.
const CostHiddenNote = "Cost is shown only to members who hold the billing:read permission in the organization; ask an owner to grant it."

// canSeeOrgCost reports whether the caller may see what the org paid, in
// the team view and an admin's drill-down: the rule GetOrgLLMUsage
// (internal/handlers/org_usage) applies to the org's spend. The caller holds
// billing:read in orgID, read from control-plane now, through a person's
// credential, or an access token that carries billing:read itself; any other
// credential sees no cost. It refuses nothing: a caller who may not see cost
// still gets the rest of the answer. An outage is an error, never a guess.
func canSeeOrgCost(ctx context.Context, userID, orgID string) (bool, error) {
	if p, isMachine := auth.MachineTokenFromContext(ctx); isMachine {
		if p.Require(fat.ScopeBillingRead) != nil {
			return false, nil
		}
	} else if auth.RequirePerson(ctx, "see the organization's cost") != nil {
		return false, nil
	}
	switch err := authz.RequireOrgScope(ctx, userID, orgID, string(fat.ScopeBillingRead)); {
	case err == nil:
		return true, nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return false, svcerr.Wrap(orgs.LookupFailed(ctx, "could not confirm your permissions in this organization; try again", err))
	default:
		return false, nil
	}
}

// hideModelCost blanks every cost figure of a section, keeping the call
// counts: reported and estimated dollars are withheld, never zeroed.
func hideModelCost(sec *pb.InsightsModelSection) {
	strip := func(st *pb.InsightsModelStats) {
		if c := st.GetCost(); c != nil {
			c.ReportedUsdNanos, c.EstimatedUsdNanos = nil, nil
		}
	}
	for _, b := range sec.GetBuckets() {
		strip(b.GetStats())
	}
	strip(sec.GetOther())
	strip(sec.GetTotals())
}
