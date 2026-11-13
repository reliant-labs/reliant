// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The stamp a trigger write decides (org-provider-accounts §11.3, I4): a new
// trigger in a team org is org_only, in a solo org any, any when the count is
// unknown (an old control plane); the owner can always tighten; loosening is
// refused in a team org; an update that does not name it keeps it.
func TestUnattendedFundingFor(t *testing.T) {
	const (
		unspecified = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED
		orgOnly     = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY
		anySource   = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY
	)
	org := "org-acme"
	stamped := func(f core.UnattendedFunding) *core.Trigger { return &core.Trigger{UnattendedFunding: f} }
	for _, tc := range []struct {
		name     string
		orgID    *string
		members  int
		known    bool
		countErr error
		chosen   reliantv1.UnattendedFunding
		existing *core.Trigger
		want     core.UnattendedFunding
		refused  bool
	}{
		{name: "a new trigger in a two-member org is org_only", orgID: &org, members: 2, known: true, chosen: unspecified, want: core.UnattendedFundingOrgOnly},
		{name: "a new trigger in a solo org is any", orgID: &org, members: 1, known: true, chosen: unspecified, want: core.UnattendedFundingAny},
		{name: "a new trigger with an old control plane is any", orgID: &org, chosen: unspecified, want: core.UnattendedFundingAny},
		{name: "a trigger with no org has no stamp", chosen: orgOnly, want: ""},
		{name: "an update that names none keeps org_only", orgID: &org, members: 1, known: true, chosen: unspecified,
			existing: stamped(core.UnattendedFundingOrgOnly), want: core.UnattendedFundingOrgOnly},
		{name: "an update that names none keeps an unstamped trigger unstamped", orgID: &org, members: 5, known: true, chosen: unspecified,
			existing: stamped(""), want: ""},
		{name: "the owner can always tighten", orgID: &org, members: 5, known: true, chosen: orgOnly,
			existing: stamped(core.UnattendedFundingAny), want: core.UnattendedFundingOrgOnly},
		{name: "loosening in a team org is refused", orgID: &org, members: 2, known: true, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), refused: true},
		{name: "loosening with an unknown count is refused", orgID: &org, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), refused: true},
		{name: "loosening in a solo org is allowed", orgID: &org, members: 1, known: true, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), want: core.UnattendedFundingAny},
		{name: "a new trigger asking for any in a team org is refused", orgID: &org, members: 3, known: true, chosen: anySource, refused: true},
		{name: "naming any on a trigger already any is no loosening", orgID: &org, members: 3, known: true, chosen: anySource,
			existing: stamped(""), want: core.UnattendedFundingAny},
		{name: "a count that cannot be read is retried, never stamped any", orgID: &org, countErr: errors.New("down"), chosen: unspecified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{deps: Deps{Orgs: &MockOrgResolver{
				LiveMembersFreshFunc: func(_ context.Context, userID, orgID string) (int, bool, error) {
					assert.Equal(t, "owner", userID)
					assert.Equal(t, org, orgID)
					return tc.members, tc.known, tc.countErr
				},
			}}}
			got, err := s.unattendedFundingFor(context.Background(), "owner", tc.orgID, tc.chosen, tc.existing)
			switch {
			case tc.refused:
				require.Error(t, err)
				assert.Equal(t, ReasonOrgOnlyAutomation, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			case tc.countErr != nil:
				require.Error(t, err)
				assert.Empty(t, got)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
