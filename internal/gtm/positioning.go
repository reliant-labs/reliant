// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/store"
)

const maxAudienceLen = 200

// GetPositioning returns the org's positioning, revision 0 when none.
func (s *service) GetPositioning(ctx context.Context, actor Actor) (Positioning, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Positioning{}, err
	}
	row, err := s.store.GetPositioning(ctx, actor.OrgID)
	if errors.Is(err, ErrNotFound) {
		return Positioning{OrgID: actor.OrgID}, nil
	}
	if err != nil {
		return Positioning{}, err
	}
	return positioningFromRow(row), nil
}

// UpdatePositioning replaces the org's positioning whole, recording who.
// A person and an agent alike may.
func (s *service) UpdatePositioning(ctx context.Context, actor Actor, in UpdatePositioningInput) (Positioning, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Positioning{}, err
	}
	if in.ExpectedRevision < 0 {
		return Positioning{}, invalid("expected_revision", "0 or more")
	}
	if err := checkLen("positioning_md", in.PositioningMD, 0, MaxBodyLen); err != nil {
		return Positioning{}, err
	}
	if len(in.Audiences) > MaxAudiences {
		return Positioning{}, invalid("audiences", fmt.Sprintf("at most %d", MaxAudiences))
	}
	for _, a := range in.Audiences {
		if err := checkLen("audiences", a, 1, maxAudienceLen); err != nil {
			return Positioning{}, err
		}
	}
	if err := checkBusiness(in.BusinessType, in.BusinessModels); err != nil {
		return Positioning{}, err
	}
	models := make([]string, 0, len(in.BusinessModels))
	for _, m := range in.BusinessModels {
		models = append(models, string(m))
	}
	org := actor.OrgID
	var out Positioning
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetPositioning(ctx, org)
		switch {
		case errors.Is(err, ErrNotFound):
			if in.ExpectedRevision != 0 {
				return conflict(org, in.ExpectedRevision, 0)
			}
			row = &store.GtmPositioning{OrgID: org, Revision: 1}
		case err != nil:
			return err
		case row.Revision != in.ExpectedRevision:
			return conflict(org, in.ExpectedRevision, row.Revision)
		}
		row.PositioningMd = in.PositioningMD
		row.Audiences = slices.Clone(in.Audiences)
		row.BusinessType, row.BusinessModels = string(in.BusinessType), models
		row.UpdatedBy = actor.UserID
		row.UpdatedAt = s.now()
		current := func() (int32, error) {
			cur, err := s.store.GetPositioning(ctx, org)
			if err != nil {
				return 0, err
			}
			return cur.Revision, nil
		}
		if in.ExpectedRevision == 0 {
			err = s.store.CreatePositioning(ctx, row)
		} else {
			err = s.store.UpdatePositioning(ctx, row, in.ExpectedRevision)
		}
		if err != nil {
			return s.writeErr(err, org, in.ExpectedRevision, current)
		}
		stored, err := s.store.GetPositioning(ctx, org)
		if err != nil {
			return err
		}
		out = positioningFromRow(stored)
		return nil
	})
	return out, err
}

// checkBusiness is the profile rule: a known business type or none, and
// known business models, each once.
func checkBusiness(t BusinessType, models []BusinessModel) error {
	if t != "" && !slices.Contains(businessTypes, t) {
		return invalid("business_type", "unknown business type")
	}
	seen := map[BusinessModel]bool{}
	for _, m := range models {
		if !slices.Contains(businessModels, m) {
			return invalid("business_models", fmt.Sprintf("unknown business model %q", m))
		}
		if seen[m] {
			return invalid("business_models", fmt.Sprintf("%q twice", m))
		}
		seen[m] = true
	}
	return nil
}
