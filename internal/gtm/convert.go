// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

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
		ID:                r.ID,
		OrgID:             r.OrgID,
		Name:              r.Name,
		Goal:              r.Goal,
		PeriodStart:       r.PeriodStart,
		PeriodEnd:         r.PeriodEnd,
		Status:            CampaignStatus(r.Status),
		StrategyID:        r.StrategyID,
		PlannedChannelIDs: slices.Clone(r.PlannedChannelIds),
		PlannedFormats:    formatsOf(r.PlannedFormats),
		Revision:          r.Revision,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
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
		Format:         ItemFormat(r.Format),
		ChannelID:      deref(r.ChannelID),
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
		PublishedURL:   deref(r.PublishedURL),
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

func formatsOf(names []string) []ItemFormat {
	out := make([]ItemFormat, 0, len(names))
	for _, n := range names {
		out = append(out, ItemFormat(n))
	}
	return out
}

func formatNames(fs []ItemFormat) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, string(f))
	}
	return out
}

// dateLayout is a strategy period's calendar date.
const dateLayout = "2006-01-02"

func dateString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(dateLayout)
}

// parseDate is "" (nil) or a YYYY-MM-DD calendar date.
func parseDate(field, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return nil, invalid(field, "a date, YYYY-MM-DD")
	}
	return &t, nil
}

func strategyFromRow(r *store.GtmStrategy) Strategy {
	return Strategy{
		ID:           r.ID,
		OrgID:        r.OrgID,
		Name:         r.Name,
		GoalMD:       r.GoalMd,
		TargetMetric: r.TargetMetric,
		PeriodStart:  dateString(r.PeriodStart),
		PeriodEnd:    dateString(r.PeriodEnd),
		Status:       StrategyStatus(r.Status),
		Category:     StrategyCategory(deref(r.Category)),
		LibraryKey:   deref(r.LibraryKey),
		Revision:     r.Revision,
		CreatedBy:    r.CreatedBy,
		UpdatedBy:    r.UpdatedBy,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

func channelFromRow(r *store.GtmChannel) Channel {
	return Channel{
		ID:                   r.ID,
		OrgID:                r.OrgID,
		Key:                  r.Key,
		Name:                 r.Name,
		Mode:                 ChannelMode(r.Mode),
		IntegrationKind:      deref(r.IntegrationKind),
		ConnectionID:         deref(r.ConnectionID),
		ManualInstructionsMD: r.ManualInstructionsMd,
		ChannelTypes:         channelTypesOf(r),
		Status:               ChannelStatus(r.Status),
		Source:               ChannelSource(r.Source),
		CreatedBy:            r.CreatedBy,
		Revision:             r.Revision,
		CreatedAt:            r.CreatedAt,
		UpdatedAt:            r.UpdatedAt,
	}
}

func assetFromRow(r *store.GtmAsset) Asset {
	a := Asset{
		ID:          r.ID,
		OrgID:       r.OrgID,
		ItemID:      deref(r.ItemID),
		CampaignID:  deref(r.CampaignID),
		ObjectKey:   r.ObjectKey,
		Filename:    r.Filename,
		ContentType: r.ContentType,
		SHA256:      deref(r.Sha256),
		Status:      AssetStatus(r.Status),
		UploadedBy:  r.UploadedBy,
		Source:      AssetSource(r.Source),
		CreatedAt:   r.CreatedAt,
	}
	if r.SizeBytes != nil {
		a.SizeBytes = *r.SizeBytes
	}
	return a
}

func assetsFromRows(rows []*store.GtmAsset) []Asset {
	out := make([]Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, assetFromRow(r))
	}
	return out
}

func int32Ptr(v int32) *int32 {
	if v == 0 {
		return nil
	}
	return &v
}

func int32Of(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func libraryFromRow(r *store.GtmStrategyLibrary) (LibraryEntry, error) {
	e := LibraryEntry{
		Key:            r.Key,
		Category:       StrategyCategory(r.Category),
		Depth:          LibraryDepth(r.Depth),
		Name:           r.Name,
		SummaryMD:      r.SummaryMd,
		Icon:           r.Icon,
		Stage:          r.Stage,
		Cost:           r.Cost,
		CostNote:       r.CostNote,
		Difficulty:     r.Difficulty,
		Level:          r.Level,
		Objective:      r.Objective,
		NorthStar:      r.NorthStar,
		HypothesisMD:   r.HypothesisMd,
		Hours:          int32Of(r.Hours),
		SignalDays:     int32Of(r.SignalDays),
		WindowDays:     int32Of(r.WindowDays),
		LaunchDays:     int32Of(r.LaunchDays),
		Lifecycle:      slices.Clone(r.Lifecycle),
		ChannelTypes:   slices.Clone(r.ChannelTypes),
		Integrations:   slices.Clone(r.Integrations),
		Fits:           slices.Clone(r.Fits),
		Prerequisites:  slices.Clone(r.Prerequisites),
		KPIs:           slices.Clone(r.Kpis),
		Tactics:        slices.Clone(r.Tactics),
		Automates:      slices.Clone(r.Automates),
		Experiments:    slices.Clone(r.Experiments),
		StopWhen:       slices.Clone(r.StopWhen),
		SourceRevision: r.SourceRevision,
		ImportedAt:     r.ImportedAt,
		ImportedBy:     r.ImportedBy,
	}
	if r.ExpectedDelta != nil {
		e.ExpectedDelta = *r.ExpectedDelta
	}
	if len(r.Steps) > 0 {
		if err := json.Unmarshal(r.Steps, &e.Steps); err != nil {
			return LibraryEntry{}, fmt.Errorf("library entry %s steps: %w", r.Key, err)
		}
	}
	if len(r.Extra) > 0 {
		if err := json.Unmarshal(r.Extra, &e.Extra); err != nil {
			return LibraryEntry{}, fmt.Errorf("library entry %s extra: %w", r.Key, err)
		}
	}
	return e, nil
}

// libraryRow is e's row for orgID; steps and extra are marshalled.
func libraryRow(orgID string, e LibraryEntry) (*store.GtmStrategyLibrary, error) {
	steps := e.Steps
	if steps == nil {
		steps = []LibraryStep{}
	}
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		return nil, fmt.Errorf("library entry %s steps: %w", e.Key, err)
	}
	extra := e.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return nil, invalid("entries.extra", "not JSON-encodable")
	}
	r := &store.GtmStrategyLibrary{
		OrgID:         orgID,
		Key:           e.Key,
		Category:      string(e.Category),
		Depth:         string(e.Depth),
		Name:          e.Name,
		SummaryMd:     e.SummaryMD,
		Icon:          e.Icon,
		Stage:         e.Stage,
		Cost:          e.Cost,
		CostNote:      e.CostNote,
		Difficulty:    e.Difficulty,
		Level:         e.Level,
		Objective:     e.Objective,
		NorthStar:     e.NorthStar,
		HypothesisMd:  e.HypothesisMD,
		Hours:         int32Ptr(e.Hours),
		SignalDays:    int32Ptr(e.SignalDays),
		WindowDays:    int32Ptr(e.WindowDays),
		LaunchDays:    int32Ptr(e.LaunchDays),
		Lifecycle:     nonNil(e.Lifecycle),
		ChannelTypes:  nonNil(e.ChannelTypes),
		Integrations:  nonNil(e.Integrations),
		Fits:          nonNil(e.Fits),
		Prerequisites: nonNil(e.Prerequisites),
		Kpis:          nonNil(e.KPIs),
		Tactics:       nonNil(e.Tactics),
		Automates:     nonNil(e.Automates),
		Experiments:   nonNil(e.Experiments),
		StopWhen:      nonNil(e.StopWhen),
		Steps:         stepsJSON,
		Extra:         extraJSON,
	}
	if e.ExpectedDelta != 0 {
		d := e.ExpectedDelta
		r.ExpectedDelta = &d
	}
	return r, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
