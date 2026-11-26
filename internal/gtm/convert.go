// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/store"
)

// Converters between the domain types and the internal/store rows. The
// handler's proto converters live in internal/handlers/gtm.

func ideaFromRow(r *store.GtmIdea) (Idea, error) {
	var ev []Evidence
	if len(r.Evidence) > 0 {
		if err := json.Unmarshal(r.Evidence, &ev); err != nil {
			return Idea{}, fmt.Errorf("idea %s evidence: %w", r.ID, err)
		}
	}
	return Idea{
		ID:           r.ID,
		OrgID:        r.OrgID,
		Kind:         IdeaKind(r.Kind),
		Title:        r.Title,
		BodyMD:       r.BodyMd,
		Evidence:     ev,
		Status:       IdeaStatus(r.Status),
		Source:       IdeaSource(r.Source),
		SourceChatID: r.SourceChatID,
		CreatedBy:    r.CreatedBy,
		CampaignID:   deref(r.CampaignID),
		Revision:     r.Revision,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}, nil
}

func ideasFromRows(rows []*store.GtmIdea) ([]Idea, error) {
	out := make([]Idea, 0, len(rows))
	for _, r := range rows {
		i, err := ideaFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

func evidenceJSON(ev []Evidence) (json.RawMessage, error) {
	if ev == nil {
		ev = []Evidence{}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("evidence: %w", err)
	}
	return b, nil
}

func campaignFromRow(r *store.GtmCampaign) Campaign {
	return Campaign{
		ID:          r.ID,
		OrgID:       r.OrgID,
		Name:        r.Name,
		Goal:        r.Goal,
		PeriodStart: r.PeriodStart,
		PeriodEnd:   r.PeriodEnd,
		Status:      CampaignStatus(r.Status),
		Revision:    r.Revision,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func campaignsFromRows(rows []*store.GtmCampaign) []Campaign {
	out := make([]Campaign, 0, len(rows))
	for _, r := range rows {
		out = append(out, campaignFromRow(r))
	}
	return out
}

func itemFromRow(r *store.GtmItem) Item {
	return Item{
		ID:             r.ID,
		OrgID:          r.OrgID,
		CampaignID:     deref(r.CampaignID),
		IdeaID:         deref(r.IdeaID),
		Kind:           ItemKind(r.Kind),
		Channel:        r.Channel,
		Title:          r.Title,
		BodyMD:         r.BodyMd,
		Status:         ItemStatus(r.Status),
		ScheduledAt:    r.ScheduledAt,
		EmailTo:        slices.Clone(r.EmailTo),
		EmailSubject:   r.EmailSubject,
		GmailMessageID: r.GmailMessageID,
		SentAt:         r.SentAt,
		SentBy:         r.SentBy,
		ApprovalHash:   r.ApprovalHash,
		Revision:       r.Revision,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func itemsFromRows(rows []*store.GtmItem) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, itemFromRow(r))
	}
	return out
}

func positioningFromRow(r *store.GtmPositioning) Positioning {
	return Positioning{
		OrgID:         r.OrgID,
		PositioningMD: r.PositioningMd,
		Audiences:     slices.Clone(r.Audiences),
		UpdatedBy:     r.UpdatedBy,
		Revision:      r.Revision,
		UpdatedAt:     r.UpdatedAt,
	}
}

func claimFromRow(r *store.GtmClaim) Claim {
	return Claim{
		ID:                 r.ID,
		OrgID:              r.OrgID,
		Text:               r.Text,
		TextNorm:           deref(r.TextNorm),
		Category:           ClaimCategory(r.Category),
		Status:             ClaimStatus(r.Status),
		SourceURL:          r.SourceURL,
		SourceKind:         ClaimSourceKind(r.SourceKind),
		VerifiedAt:         r.VerifiedAt,
		VerifiedBy:         r.VerifiedBy,
		ExpiresOrRecheckAt: r.ExpiresOrRecheckAt,
		Notes:              r.Notes,
		Revision:           r.Revision,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
}

func claimsFromRows(rows []*store.GtmClaim) []Claim {
	out := make([]Claim, 0, len(rows))
	for _, r := range rows {
		out = append(out, claimFromRow(r))
	}
	return out
}
