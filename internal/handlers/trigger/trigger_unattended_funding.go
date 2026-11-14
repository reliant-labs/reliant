// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"slices"

	"github.com/reliant-labs/forge/pkg/svcerr"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// ReasonOrgOnlyAutomation is the x-forge-error-reason of a write that would
// loosen an automation in a team org from the org's own accounts to the
// owner's (org-provider-accounts §11.3).
const ReasonOrgOnlyAutomation = "org_only_automation"

// unattendedFundingFor decides a trigger's stamp: which AI accounts the runs
// it fires may spend (org-provider-accounts §11.3, I4). orgID is the org the
// write decided for the trigger, existing the stored row of an update.
//
//   - A trigger with no org has no stamp: it reads as any.
//   - UNSPECIFIED is the default rule. A new trigger is stamped from its
//     org's shape: org_only when the org has two or more live members, any
//     otherwise, and any when the count is unknown (an old control plane: a
//     default, never a broken automation). An update keeps the stored stamp,
//     so a client that predates the field never changes it.
//   - ORG_ONLY is always allowed: the owner can always tighten.
//   - ANY over org_only, or on a new trigger, is a loosening. It is allowed
//     in an org this read shows is solo, or when the org's policy admits
//     members' own accounts for automations (policyAdmitsPersonalAutomations);
//     otherwise it is refused with ReasonOrgOnlyAutomation.
func (s *Service) unattendedFundingFor(ctx context.Context, ownerID string, orgID *string, chosen reliantv1.UnattendedFunding, existing *core.Trigger) (core.UnattendedFunding, error) {
	if orgID == nil || *orgID == "" {
		return "", nil
	}
	want, picked := triggers.UnattendedFundingFromProto(chosen)
	switch {
	case !picked && existing != nil:
		return existing.UnattendedFunding, nil
	case !picked:
		n, known, err := s.liveMembers(ctx, ownerID, *orgID)
		if err != nil {
			return "", err
		}
		if known && n >= 2 {
			return core.UnattendedFundingOrgOnly, nil
		}
		return core.UnattendedFundingAny, nil
	case want.OrgOnly():
		return core.UnattendedFundingOrgOnly, nil
	case existing != nil && !existing.UnattendedFunding.OrgOnly():
		// Already any (or unstamped, which reads as any): nothing loosens.
		return core.UnattendedFundingAny, nil
	}
	n, known, err := s.liveMembers(ctx, ownerID, *orgID)
	if err != nil {
		return "", err
	}
	if (!known || n >= 2) && !policyAdmitsPersonalAutomations(ctx, *orgID) {
		return "", svcerr.WithReason(svcerr.FailedPrecondition(
			"automations in this organization run on its own AI accounts only; "+
				"an organization with other members cannot let an automation spend its owner's personal accounts"),
			ReasonOrgOnlyAutomation)
	}
	return core.UnattendedFundingAny, nil
}

// liveMembers is how many live members orgID has, asked of control-plane now
// as ownerID; known is false when it gives no count (or there are no orgs).
func (s *Service) liveMembers(ctx context.Context, ownerID, orgID string) (int, bool, error) {
	if s.deps.Orgs == nil {
		return 0, false, nil
	}
	n, known, err := s.deps.Orgs.LiveMembersFresh(ctx, ownerID, orgID)
	if err != nil {
		return 0, false, svcerr.WithCause(svcerr.Unavailable("could not read the organization's members; try again"), err)
	}
	return n, known, nil
}

// policyAdmitsPersonalAutomations reports whether orgID's policy, read now
// through orgpolicy.For, admits members' own accounts for its automations
// (org-provider-accounts §11.3, §11.6): its automations section names PERSONAL,
// and its providers section, when it names sources, does too. Then an admin
// has chosen to let automations spend them, so the owner may loosen theirs.
// A policy that cannot be read admits nothing: the loosening is refused, as
// it was before the policy existed.
func policyAdmitsPersonalAutomations(ctx context.Context, orgID string) bool {
	pol, err := orgpolicy.For(ctx, orgID)
	if err != nil {
		return false
	}
	personal := controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL
	providers := pol.Doc.GetProviders().GetAllowedSources()
	return slices.Contains(pol.Doc.GetAutomations().GetAllowedSources(), personal) &&
		(len(providers) == 0 || slices.Contains(providers, personal))
}
