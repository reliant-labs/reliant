// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

var (
	strategyStatuses   = []StrategyStatus{StrategyDraft, StrategyActive, StrategyDone, StrategyArchived}
	strategyCategories = []StrategyCategory{
		CategoryPricingOffersIncentives, CategoryReferralViralGrowth, CategoryPaidAcquisition,
		CategoryOrganicContentSEO, CategoryOutboundSalesProspecting, CategoryPartnershipsDistribution,
		CategoryLaunchesPR, CategoryCommunityEvents, CategoryProductLedGrowth, CategoryConversionOptimization,
		CategoryRetentionExpansion, CategoryTrustReputation, CategoryMarketResearchPositioning,
	}
)

const (
	maxStrategyNameLen = 200
	maxTargetMetricLen = 500
)

// ListStrategies lists the org's strategies, newest first.
func (s *service) ListStrategies(ctx context.Context, actor Actor, in ListStrategiesInput) (ListStrategiesResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListStrategiesResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultStrategyPageSize, MaxStrategyPageSize)
	if err != nil {
		return ListStrategiesResult{}, err
	}
	if in.Status != "" && !slices.Contains(strategyStatuses, in.Status) {
		return ListStrategiesResult{}, invalid("status", "unknown status")
	}
	rows, err := s.store.ListStrategies(ctx, StrategyFilter{OrgID: actor.OrgID, Status: in.Status, Limit: limit, Offset: offset})
	if err != nil {
		return ListStrategiesResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	out := make([]Strategy, 0, len(rows))
	for _, r := range rows {
		out = append(out, strategyFromRow(r))
	}
	return ListStrategiesResult{Strategies: out, NextPageToken: next}, nil
}

// GetStrategy returns a strategy with its campaigns, newest first, capped.
func (s *service) GetStrategy(ctx context.Context, actor Actor, in GetStrategyInput) (StrategyDetail, error) {
	if err := s.admit(ctx, actor); err != nil {
		return StrategyDetail{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return StrategyDetail{}, err
	}
	row, err := s.store.GetStrategy(ctx, actor.OrgID, in.ID)
	if err != nil {
		return StrategyDetail{}, s.notFound(ctx, err, "strategy", in.ID)
	}
	campaigns, err := s.store.ListCampaigns(ctx, CampaignFilter{OrgID: actor.OrgID, StrategyID: in.ID, Limit: MaxStrategyCampaigns + 1})
	if err != nil {
		return StrategyDetail{}, err
	}
	truncated := len(campaigns) > MaxStrategyCampaigns
	campaigns = campaigns[:min(len(campaigns), MaxStrategyCampaigns)]
	return StrategyDetail{Strategy: strategyFromRow(row), Campaigns: campaignsFromRows(campaigns), Truncated: truncated}, nil
}

// CreateStrategy creates a strategy; an agent's is always DRAFT.
func (s *service) CreateStrategy(ctx context.Context, actor Actor, in CreateStrategyInput) (Strategy, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Strategy{}, err
	}
	status := in.Status
	if status == "" {
		status = StrategyDraft
	}
	if !slices.Contains(strategyStatuses, status) {
		return Strategy{}, invalid("status", "unknown status")
	}
	if status != StrategyDraft && !actor.Person() {
		return Strategy{}, personOnly("create a strategy that is not a draft")
	}
	if in.Category != "" && !slices.Contains(strategyCategories, in.Category) {
		return Strategy{}, invalid("category", "unknown category")
	}
	if err := firstErr(
		checkLen("name", in.Name, 1, maxStrategyNameLen),
		checkLen("goal_md", in.GoalMD, 0, MaxBodyLen),
		checkLen("target_metric", in.TargetMetric, 0, maxTargetMetricLen),
	); err != nil {
		return Strategy{}, err
	}
	start, end, err := strategyPeriod(in.PeriodStart, in.PeriodEnd)
	if err != nil {
		return Strategy{}, err
	}
	now := s.now()
	row := &store.GtmStrategy{
		OrgID:        actor.OrgID,
		Name:         in.Name,
		GoalMd:       in.GoalMD,
		TargetMetric: in.TargetMetric,
		PeriodStart:  start,
		PeriodEnd:    end,
		Status:       string(status),
		Category:     strPtrOrNil(string(in.Category)),
		Revision:     1,
		CreatedBy:    actor.UserID,
		UpdatedBy:    actor.UserID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	var out Strategy
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		r := *row
		if err := s.store.CreateStrategy(ctx, &r); err != nil {
			return err
		}
		out = strategyFromRow(&r)
		return nil
	})
	return out, err
}

// UpdateStrategy edits a strategy; an agent edits DRAFT ones and never
// moves a status.
func (s *service) UpdateStrategy(ctx context.Context, actor Actor, in UpdateStrategyInput) (Strategy, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Strategy{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision)); err != nil {
		return Strategy{}, err
	}
	var checks []error
	if in.Name != nil {
		checks = append(checks, checkLen("name", *in.Name, 1, maxStrategyNameLen))
	}
	if in.GoalMD != nil {
		checks = append(checks, checkLen("goal_md", *in.GoalMD, 0, MaxBodyLen))
	}
	if in.TargetMetric != nil {
		checks = append(checks, checkLen("target_metric", *in.TargetMetric, 0, maxTargetMetricLen))
	}
	if in.Status != nil && !slices.Contains(strategyStatuses, *in.Status) {
		checks = append(checks, invalid("status", "unknown status"))
	}
	if in.Category != nil && *in.Category != "" && !slices.Contains(strategyCategories, *in.Category) {
		checks = append(checks, invalid("category", "unknown category"))
	}
	if err := firstErr(checks...); err != nil {
		return Strategy{}, err
	}
	var out Strategy
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetStrategy(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "strategy", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if !actor.Person() {
			if StrategyStatus(row.Status) != StrategyDraft {
				return personOnly("change a strategy that is not a draft")
			}
			if in.Status != nil && *in.Status != StrategyDraft {
				return personOnly("move a strategy to " + statusName(string(*in.Status), "GTM_STRATEGY_STATUS_"))
			}
		}
		if in.Name != nil {
			row.Name = *in.Name
		}
		if in.GoalMD != nil {
			row.GoalMd = *in.GoalMD
		}
		if in.TargetMetric != nil {
			row.TargetMetric = *in.TargetMetric
		}
		start, end := dateString(row.PeriodStart), dateString(row.PeriodEnd)
		if in.PeriodStart != nil {
			start = *in.PeriodStart
		}
		if in.PeriodEnd != nil {
			end = *in.PeriodEnd
		}
		if row.PeriodStart, row.PeriodEnd, err = strategyPeriod(start, end); err != nil {
			return err
		}
		if in.Status != nil {
			row.Status = string(*in.Status)
		}
		if in.Category != nil {
			row.Category = strPtrOrNil(string(*in.Category))
		}
		row.UpdatedBy = actor.UserID
		row.UpdatedAt = s.now()
		if err := s.store.UpdateStrategy(ctx, row, in.ExpectedRevision); err != nil {
			return s.writeErr(err, in.ID, in.ExpectedRevision, func() (int32, error) {
				cur, err := s.store.GetStrategy(ctx, actor.OrgID, in.ID)
				if err != nil {
					return 0, err
				}
				return cur.Revision, nil
			})
		}
		stored, err := s.store.GetStrategy(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "strategy", in.ID)
		}
		out = strategyFromRow(stored)
		return nil
	})
	return out, err
}

// strategyPeriod parses a period of real calendar dates, end not before
// start.
func strategyPeriod(start, end string) (*time.Time, *time.Time, error) {
	s, err := parseDate("period_start", start)
	if err != nil {
		return nil, nil, err
	}
	e, err := parseDate("period_end", end)
	if err != nil {
		return nil, nil, err
	}
	if s != nil && e != nil && e.Before(*s) {
		return nil, nil, invalid("period_end", "before period_start")
	}
	return s, e, nil
}

// checkStrategyLink is rule 2 for a campaign's strategy: a strategy of the
// org (every campaign has one).
func (s *service) checkStrategyLink(ctx context.Context, orgID, id string) error {
	if id == "" {
		return invalid("strategy_id", "required")
	}
	if _, err := s.store.GetStrategy(ctx, orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &LinkError{Field: "strategy_id", ID: id}
		}
		return err
	}
	return nil
}
