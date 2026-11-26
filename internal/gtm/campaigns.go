// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

var campaignStatuses = []CampaignStatus{CampaignDraft, CampaignActive, CampaignDone, CampaignArchived}

// ListCampaigns lists the org's campaigns, newest first.
func (s *service) ListCampaigns(ctx context.Context, actor Actor, in ListCampaignsInput) (ListCampaignsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListCampaignsResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultCampaignPageSize, MaxCampaignPageSize)
	if err != nil {
		return ListCampaignsResult{}, err
	}
	if in.Status != "" && !slices.Contains(campaignStatuses, in.Status) {
		return ListCampaignsResult{}, invalid("status", "unknown status")
	}
	rows, err := s.store.ListCampaigns(ctx, CampaignFilter{OrgID: actor.OrgID, Status: in.Status, Limit: limit, Offset: offset})
	if err != nil {
		return ListCampaignsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	return ListCampaignsResult{Campaigns: campaignsFromRows(rows), NextPageToken: next}, nil
}

// GetCampaign returns a campaign with its linked ideas and items.
func (s *service) GetCampaign(ctx context.Context, actor Actor, in GetCampaignInput) (CampaignDetail, error) {
	if err := s.admit(ctx, actor); err != nil {
		return CampaignDetail{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return CampaignDetail{}, err
	}
	row, err := s.store.GetCampaign(ctx, actor.OrgID, in.ID)
	if err != nil {
		return CampaignDetail{}, s.notFound(ctx, err, "campaign", in.ID)
	}
	// One row past each cap says whether the list was cut.
	ideaRows, err := s.store.ListIdeas(ctx, IdeaFilter{OrgID: actor.OrgID, CampaignID: &in.ID, Limit: MaxCampaignIdeas + 1})
	if err != nil {
		return CampaignDetail{}, err
	}
	itemRows, err := s.store.ListItems(ctx, ItemFilter{OrgID: actor.OrgID, CampaignID: &in.ID, Limit: MaxCampaignItems + 1})
	if err != nil {
		return CampaignDetail{}, err
	}
	truncated := len(ideaRows) > MaxCampaignIdeas || len(itemRows) > MaxCampaignItems
	ideaRows = ideaRows[:min(len(ideaRows), MaxCampaignIdeas)]
	itemRows = itemRows[:min(len(itemRows), MaxCampaignItems)]
	ideas, err := ideasFromRows(ideaRows)
	if err != nil {
		return CampaignDetail{}, err
	}
	return CampaignDetail{Campaign: campaignFromRow(row), Ideas: ideas, Items: itemsFromRows(itemRows), Truncated: truncated}, nil
}

// CreateCampaign creates a campaign, DRAFT unless a status is given.
func (s *service) CreateCampaign(ctx context.Context, actor Actor, in CreateCampaignInput) (Campaign, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Campaign{}, err
	}
	status := in.Status
	if status == "" {
		status = CampaignDraft
	}
	if !slices.Contains(campaignStatuses, status) {
		return Campaign{}, invalid("status", "unknown status")
	}
	start, end := utcPtr(in.PeriodStart), utcPtr(in.PeriodEnd)
	if err := firstErr(
		checkLen("name", in.Name, 1, MaxCampaignNameLen),
		checkLen("goal", in.Goal, 0, MaxShortTextLen),
		checkPeriod(start, end),
	); err != nil {
		return Campaign{}, err
	}
	now := s.now()
	var out Campaign
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row := &store.GtmCampaign{
			OrgID:       actor.OrgID,
			Name:        in.Name,
			Goal:        in.Goal,
			PeriodStart: start,
			PeriodEnd:   end,
			Status:      string(status),
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.store.CreateCampaign(ctx, row); err != nil {
			return err
		}
		out = campaignFromRow(row)
		return nil
	})
	return out, err
}

// UpdateCampaign changes a campaign's fields.
func (s *service) UpdateCampaign(ctx context.Context, actor Actor, in UpdateCampaignInput) (Campaign, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Campaign{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision)); err != nil {
		return Campaign{}, err
	}
	if in.Name != nil {
		if err := checkLen("name", *in.Name, 1, MaxCampaignNameLen); err != nil {
			return Campaign{}, err
		}
	}
	if in.Goal != nil {
		if err := checkLen("goal", *in.Goal, 0, MaxShortTextLen); err != nil {
			return Campaign{}, err
		}
	}
	if in.Status != nil && !slices.Contains(campaignStatuses, *in.Status) {
		return Campaign{}, invalid("status", "unknown status")
	}
	var out Campaign
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetCampaign(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "campaign", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if in.Name != nil {
			row.Name = *in.Name
		}
		if in.Goal != nil {
			row.Goal = *in.Goal
		}
		switch {
		case in.ClearPeriodStart:
			row.PeriodStart = nil
		case in.PeriodStart != nil:
			row.PeriodStart = utcPtr(in.PeriodStart)
		}
		switch {
		case in.ClearPeriodEnd:
			row.PeriodEnd = nil
		case in.PeriodEnd != nil:
			row.PeriodEnd = utcPtr(in.PeriodEnd)
		}
		if err := checkPeriod(row.PeriodStart, row.PeriodEnd); err != nil {
			return err
		}
		if in.Status != nil {
			row.Status = string(*in.Status)
		}
		row.UpdatedAt = s.now()
		if err := s.store.UpdateCampaign(ctx, row, in.ExpectedRevision); err != nil {
			return s.writeErr(err, in.ID, in.ExpectedRevision, func() (int32, error) {
				cur, err := s.store.GetCampaign(ctx, actor.OrgID, in.ID)
				if err != nil {
					return 0, err
				}
				return cur.Revision, nil
			})
		}
		stored, err := s.store.GetCampaign(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "campaign", in.ID)
		}
		out = campaignFromRow(stored)
		return nil
	})
	return out, err
}

func checkPeriod(start, end *time.Time) error {
	if start != nil && end != nil && end.Before(*start) {
		return invalid("period_end", "before period_start")
	}
	return nil
}
