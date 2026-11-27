// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
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
	f := CampaignFilter{OrgID: actor.OrgID, Status: in.Status, Limit: limit, Offset: offset}
	if in.StrategyID != nil {
		if *in.StrategyID == "" {
			return ListCampaignsResult{}, invalid("strategy_id", "empty")
		}
		f.StrategyID = *in.StrategyID
	}
	rows, err := s.store.ListCampaigns(ctx, f)
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
	counts, err := s.store.CampaignItemCounts(ctx, actor.OrgID, in.ID)
	if err != nil {
		return CampaignDetail{}, err
	}
	campaign := campaignFromRow(row)
	return CampaignDetail{
		Campaign:  campaign,
		Ideas:     ideas,
		Items:     itemsFromRows(itemRows),
		Truncated: truncated,
		Coverage:  coverage(campaign.PlannedChannelIDs, campaign.PlannedFormats, counts),
	}, nil
}

// coverage holds a campaign's plan against its items. The planned pairs
// come first: every planned channel x every planned format; with a
// one-sided plan, each planned channel (or format) with the other side
// unset. An item counts on its exact pair, else on its one-sided planned
// row, else on an unplanned row of its own, appended in (channel, format)
// order.
func coverage(channels []string, formats []ItemFormat, counts []ItemCount) []CoverageRow {
	var rows []CoverageRow
	index := map[[2]string]int{}
	add := func(ch string, f ItemFormat, planned bool) int {
		key := [2]string{ch, string(f)}
		if i, ok := index[key]; ok {
			return i
		}
		index[key] = len(rows)
		rows = append(rows, CoverageRow{ChannelID: ch, Format: f, Planned: planned})
		return len(rows) - 1
	}
	switch {
	case len(channels) > 0 && len(formats) > 0:
		for _, ch := range channels {
			for _, f := range formats {
				add(ch, f, true)
			}
		}
	case len(channels) > 0:
		for _, ch := range channels {
			add(ch, "", true)
		}
	case len(formats) > 0:
		for _, f := range formats {
			add("", f, true)
		}
	}
	byRow := map[int]map[ItemStatus]int{}
	for _, c := range counts {
		i, ok := index[[2]string{c.ChannelID, string(c.Format)}]
		if !ok && len(formats) == 0 && c.ChannelID != "" {
			i, ok = index[[2]string{c.ChannelID, ""}]
		}
		if !ok && len(channels) == 0 {
			i, ok = index[[2]string{"", string(c.Format)}]
		}
		if !ok {
			i = add(c.ChannelID, c.Format, false)
		}
		if byRow[i] == nil {
			byRow[i] = map[ItemStatus]int{}
		}
		byRow[i][c.Status] += c.Count
	}
	for i := range rows {
		for _, st := range itemStatuses {
			if n := byRow[i][st]; n > 0 {
				rows[i].Counts = append(rows[i].Counts, StatusCount{Status: st, Count: n})
				rows[i].Total += n
			}
		}
	}
	return rows
}

// checkPlan validates a campaign's planned channels (each a channel of the
// org, once) and formats (each known, once).
func (s *service) checkPlan(ctx context.Context, orgID string, channels []string, formats []ItemFormat) error {
	if len(channels) > MaxPlannedChannels {
		return invalid("planned_channel_ids", "at most 50")
	}
	seen := map[string]bool{}
	for _, id := range channels {
		if id == "" || seen[id] {
			return invalid("planned_channel_ids", "each a channel id, once")
		}
		seen[id] = true
		if _, err := s.store.GetChannel(ctx, orgID, id); err != nil {
			if errors.Is(err, ErrNotFound) {
				return &LinkError{Field: "planned_channel_ids", ID: id}
			}
			return err
		}
	}
	fseen := map[ItemFormat]bool{}
	for _, f := range formats {
		if !slices.Contains(itemFormats, f) || fseen[f] {
			return invalid("planned_formats", "each a known format, once")
		}
		fseen[f] = true
	}
	return nil
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
	if err := checkID("strategy_id", in.StrategyID); err != nil {
		return Campaign{}, err
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := firstErr(
			s.checkStrategyLink(ctx, actor.OrgID, in.StrategyID),
			s.checkPlan(ctx, actor.OrgID, in.PlannedChannelIDs, in.PlannedFormats),
		); err != nil {
			return err
		}
		row := &store.GtmCampaign{
			OrgID:             actor.OrgID,
			Name:              in.Name,
			Goal:              in.Goal,
			PeriodStart:       start,
			PeriodEnd:         end,
			Status:            string(status),
			StrategyID:        in.StrategyID,
			PlannedChannelIds: nonNil(in.PlannedChannelIDs),
			PlannedFormats:    formatNames(in.PlannedFormats),
			Revision:          1,
			CreatedAt:         now,
			UpdatedAt:         now,
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
	if in.StrategyID != nil && *in.StrategyID == "" {
		return Campaign{}, invalid("strategy_id", "a campaign always has a strategy")
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
		if in.StrategyID != nil {
			if err := s.checkStrategyLink(ctx, actor.OrgID, *in.StrategyID); err != nil {
				return err
			}
			row.StrategyID = *in.StrategyID
		}
		if in.PlannedChannelIDs != nil || in.PlannedFormats != nil {
			channels, formats := row.PlannedChannelIds, formatsOf(row.PlannedFormats)
			if in.PlannedChannelIDs != nil {
				channels = *in.PlannedChannelIDs
			}
			if in.PlannedFormats != nil {
				formats = *in.PlannedFormats
			}
			if err := s.checkPlan(ctx, actor.OrgID, channels, formats); err != nil {
				return err
			}
			row.PlannedChannelIds, row.PlannedFormats = nonNil(channels), formatNames(formats)
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
