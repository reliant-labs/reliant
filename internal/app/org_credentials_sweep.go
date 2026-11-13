// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgcredentials"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// OrgCredentialSweep keeps the credentials reliant holds for orgs in step with
// control-plane, hourly (internal/orgcredentials): it deletes grants that
// outlived their membership and purges the credentials of gone orgs. Every
// replica runs it; both sweeps are idempotent.
type OrgCredentialSweep struct{ sweeper *orgcredentials.Sweeper }

// OrgCredentialSweepDeps is the repository and the token authority, which is
// control-plane's AccessTokenInternalService when hosted.
type OrgCredentialSweepDeps struct {
	DB        *db.Repo
	Authority tokenauthority.Authority
}

// NewOrgCredentialSweep is the sweep, or nothing (nil) without a control
// plane: a self-hosted deployment has no orgs.
//
//forge:root-only api
func NewOrgCredentialSweep(d OrgCredentialSweepDeps) (*OrgCredentialSweep, error) {
	cp, hosted := d.Authority.(interface {
		orgcredentials.Membership
		orgcredentials.GoneOrgs
	})
	if !hosted {
		return nil, nil
	}
	return &OrgCredentialSweep{sweeper: orgcredentials.New(orgcredentials.Deps{
		Store: d.DB, Membership: cp, GoneOrgs: cp,
	})}, nil
}

// Run sweeps until ctx ends.
func (s *OrgCredentialSweep) Run(ctx context.Context) error {
	s.sweeper.Run(ctx, orgcredentials.Interval)
	return nil
}
