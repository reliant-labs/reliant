// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// Converters between the GtmService messages and the domain types. A domain
// enum is its proto value NAME ("GTM_IDEA_STATUS_PROPOSED"), and "" is unset,
// which is the proto's UNSPECIFIED zero.

// protoEnum is a generated GtmService enum.
type protoEnum interface {
	~int32
	String() string
}

// enumIn is e as a domain enum: "" for UNSPECIFIED. An undefined number
// becomes its digits, which the domain refuses (or no row matches).
func enumIn[T ~string, E protoEnum](e E) T {
	if e == 0 {
		return ""
	}
	return T(e.String())
}

// optEnumIn is enumIn for an optional field: nil stays nil (leave alone).
func optEnumIn[T ~string, E protoEnum](e *E) *T {
	if e == nil {
		return nil
	}
	v := enumIn[T](*e)
	return &v
}

// enumOut is a domain enum as its proto value; one the proto lacks is
// UNSPECIFIED.
func enumOut[E ~int32, T ~string](v T, values map[string]int32) E {
	return E(values[string(v)])
}

// count32 is a count as an int32 field, capped there (counts are far below).
func count32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

func formatsIn(fs []pb.GtmItemFormat) []gtmsvc.ItemFormat {
	if fs == nil {
		return nil
	}
	out := make([]gtmsvc.ItemFormat, 0, len(fs))
	for _, f := range fs {
		out = append(out, enumIn[gtmsvc.ItemFormat](f))
	}
	return out
}

func formatsOut(fs []gtmsvc.ItemFormat) []pb.GtmItemFormat {
	out := make([]pb.GtmItemFormat, 0, len(fs))
	for _, f := range fs {
		out = append(out, enumOut[pb.GtmItemFormat](f, pb.GtmItemFormat_value))
	}
	return out
}

// timeIn is a request timestamp; nil is unset. One that is not a valid
// timestamp is a *ValidationError naming field.
func timeIn(field string, ts *timestamppb.Timestamp) (*time.Time, error) {
	if ts == nil {
		return nil, nil
	}
	if err := ts.CheckValid(); err != nil {
		return nil, &gtmsvc.ValidationError{Field: field, Reason: "not a valid timestamp"}
	}
	t := ts.AsTime()
	return &t, nil
}

func timeOut(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func optTimeOut(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timeOut(*t)
}

func strategyOut(s gtmsvc.Strategy) *pb.GtmStrategy {
	return &pb.GtmStrategy{
		Id:           s.ID,
		OrgId:        s.OrgID,
		Name:         s.Name,
		GoalMd:       s.GoalMD,
		TargetMetric: s.TargetMetric,
		PeriodStart:  s.PeriodStart,
		PeriodEnd:    s.PeriodEnd,
		Status:       enumOut[pb.GtmStrategyStatus](s.Status, pb.GtmStrategyStatus_value),
		Revision:     s.Revision,
		CreatedBy:    s.CreatedBy,
		UpdatedBy:    s.UpdatedBy,
		CreatedAt:    timeOut(s.CreatedAt),
		UpdatedAt:    timeOut(s.UpdatedAt),
		Category:     enumOut[pb.GtmStrategyCategory](s.Category, pb.GtmStrategyCategory_value),
		LibraryKey:   s.LibraryKey,
	}
}

func strategiesOut(ss []gtmsvc.Strategy) []*pb.GtmStrategy {
	out := make([]*pb.GtmStrategy, 0, len(ss))
	for _, s := range ss {
		out = append(out, strategyOut(s))
	}
	return out
}

func channelOut(c gtmsvc.Channel) *pb.GtmChannel {
	return &pb.GtmChannel{
		Id:                   c.ID,
		OrgId:                c.OrgID,
		Key:                  c.Key,
		Name:                 c.Name,
		Mode:                 enumOut[pb.GtmChannelMode](c.Mode, pb.GtmChannelMode_value),
		IntegrationKind:      c.IntegrationKind,
		ConnectionId:         c.ConnectionID,
		ManualInstructionsMd: c.ManualInstructionsMD,
		Status:               enumOut[pb.GtmChannelStatus](c.Status, pb.GtmChannelStatus_value),
		ConnectionState:      enumOut[pb.GtmConnectionState](c.ConnectionState, pb.GtmConnectionState_value),
		Revision:             c.Revision,
		CreatedAt:            timeOut(c.CreatedAt),
		UpdatedAt:            timeOut(c.UpdatedAt),
		Source:               enumOut[pb.GtmChannelSource](c.Source, pb.GtmChannelSource_value),
		CreatedBy:            c.CreatedBy,
		ChannelTypes:         c.ChannelTypes,
	}
}

func channelsOut(cs []gtmsvc.Channel) []*pb.GtmChannel {
	out := make([]*pb.GtmChannel, 0, len(cs))
	for _, c := range cs {
		out = append(out, channelOut(c))
	}
	return out
}

func assetOut(a gtmsvc.Asset) *pb.GtmAsset {
	return &pb.GtmAsset{
		Id:          a.ID,
		OrgId:       a.OrgID,
		ItemId:      a.ItemID,
		CampaignId:  a.CampaignID,
		Filename:    a.Filename,
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
		Sha256:      a.SHA256,
		UploadedBy:  a.UploadedBy,
		Source:      enumOut[pb.GtmAssetSource](a.Source, pb.GtmAssetSource_value),
		CreatedAt:   timeOut(a.CreatedAt),
		Status:      enumOut[pb.GtmAssetStatus](a.Status, pb.GtmAssetStatus_value),
	}
}

func assetsOut(as []gtmsvc.Asset) []*pb.GtmAsset {
	out := make([]*pb.GtmAsset, 0, len(as))
	for _, a := range as {
		out = append(out, assetOut(a))
	}
	return out
}

// enumsIn is a repeated enum as domain values (nil for none).
func enumsIn[T ~string, E protoEnum](es []E) []T {
	if len(es) == 0 {
		return nil
	}
	out := make([]T, 0, len(es))
	for _, e := range es {
		out = append(out, enumIn[T](e))
	}
	return out
}

// enumsOut is domain enum values as a repeated proto enum.
func enumsOut[E ~int32, T ~string](vs []T, values map[string]int32) []E {
	out := make([]E, 0, len(vs))
	for _, v := range vs {
		out = append(out, enumOut[E](v, values))
	}
	return out
}

// libraryEntryOut is an entry of the embedded idea bank.
func libraryEntryOut(e gtmsvc.LibraryEntry) *pb.GtmStrategyLibraryEntry {
	return &pb.GtmStrategyLibraryEntry{
		Key:                 e.Key,
		Category:            enumOut[pb.GtmStrategyCategory](e.Category, pb.GtmStrategyCategory_value),
		Name:                e.Name,
		SummaryMd:           e.SummaryMD,
		WhyItWorksMd:        e.WhyItWorksMD,
		BusinessTypes:       enumsOut[pb.GtmBusinessType](e.BusinessTypes, pb.GtmBusinessType_value),
		BusinessModels:      enumsOut[pb.GtmBusinessModel](e.BusinessModels, pb.GtmBusinessModel_value),
		Stages:              e.Stages,
		Objective:           e.Objective,
		Effort:              e.Effort,
		Cost:                e.Cost,
		SignalDays:          count32(e.SignalDays),
		TailorQuestions:     e.TailorQuestions,
		SuggestedWorkflowMd: e.SuggestedWorkflowMD,
		ChannelTypes:        e.ChannelTypes,
		Kpis:                e.KPIs,
	}
}

func libraryEntriesOut(es []gtmsvc.LibraryEntry) []*pb.GtmStrategyLibraryEntry {
	out := make([]*pb.GtmStrategyLibraryEntry, 0, len(es))
	for _, e := range es {
		out = append(out, libraryEntryOut(e))
	}
	return out
}

func tailorAnswersIn(as []*pb.GtmTailorAnswer) []gtmsvc.TailorAnswer {
	out := make([]gtmsvc.TailorAnswer, 0, len(as))
	for _, a := range as {
		out = append(out, gtmsvc.TailorAnswer{Question: a.GetQuestion(), Answer: a.GetAnswer()})
	}
	return out
}

func tailorAnswersOut(as []gtmsvc.TailorAnswer) []*pb.GtmTailorAnswer {
	out := make([]*pb.GtmTailorAnswer, 0, len(as))
	for _, a := range as {
		out = append(out, &pb.GtmTailorAnswer{Question: a.Question, Answer: a.Answer})
	}
	return out
}

func evidenceIn(ev []*pb.GtmEvidence) ([]gtmsvc.Evidence, error) {
	out := make([]gtmsvc.Evidence, 0, len(ev))
	for _, e := range ev {
		observed, err := timeIn("evidence.observed_at", e.GetObservedAt())
		if err != nil {
			return nil, err
		}
		out = append(out, gtmsvc.Evidence{
			Kind:       e.GetKind(),
			Title:      e.GetTitle(),
			URL:        e.GetUrl(),
			SummaryMD:  e.GetSummaryMd(),
			ObservedAt: observed,
		})
	}
	return out, nil
}

func evidenceOut(ev []gtmsvc.Evidence) []*pb.GtmEvidence {
	out := make([]*pb.GtmEvidence, 0, len(ev))
	for _, e := range ev {
		out = append(out, &pb.GtmEvidence{
			Kind:       e.Kind,
			Title:      e.Title,
			Url:        e.URL,
			SummaryMd:  e.SummaryMD,
			ObservedAt: optTimeOut(e.ObservedAt),
		})
	}
	return out
}

func ideaOut(i gtmsvc.Idea) *pb.GtmIdea {
	return &pb.GtmIdea{
		Id:           i.ID,
		OrgId:        i.OrgID,
		Kind:         enumOut[pb.GtmIdeaKind](i.Kind, pb.GtmIdeaKind_value),
		Title:        i.Title,
		BodyMd:       i.BodyMD,
		Evidence:     evidenceOut(i.Evidence),
		Status:       enumOut[pb.GtmIdeaStatus](i.Status, pb.GtmIdeaStatus_value),
		Source:       enumOut[pb.GtmIdeaSource](i.Source, pb.GtmIdeaSource_value),
		SourceChatId: i.SourceChatID,
		CreatedBy:    i.CreatedBy,
		CampaignId:   i.CampaignID,
		Revision:     i.Revision,
		CreatedAt:    timeOut(i.CreatedAt),
		UpdatedAt:    timeOut(i.UpdatedAt),

		LibraryKey:          i.LibraryKey,
		TailorAnswers:       tailorAnswersOut(i.TailorAnswers),
		SuggestedWorkflowMd: i.SuggestedWorkflowMD,
	}
}

func ideasOut(is []gtmsvc.Idea) []*pb.GtmIdea {
	out := make([]*pb.GtmIdea, 0, len(is))
	for _, i := range is {
		out = append(out, ideaOut(i))
	}
	return out
}

func campaignOut(c gtmsvc.Campaign) *pb.GtmCampaign {
	return &pb.GtmCampaign{
		Id:                c.ID,
		OrgId:             c.OrgID,
		Name:              c.Name,
		Goal:              c.Goal,
		PeriodStart:       optTimeOut(c.PeriodStart),
		PeriodEnd:         optTimeOut(c.PeriodEnd),
		Status:            enumOut[pb.GtmCampaignStatus](c.Status, pb.GtmCampaignStatus_value),
		Revision:          c.Revision,
		CreatedAt:         timeOut(c.CreatedAt),
		UpdatedAt:         timeOut(c.UpdatedAt),
		StrategyId:        c.StrategyID,
		PlannedChannelIds: c.PlannedChannelIDs,
		PlannedFormats:    formatsOut(c.PlannedFormats),
	}
}

func campaignsOut(cs []gtmsvc.Campaign) []*pb.GtmCampaign {
	out := make([]*pb.GtmCampaign, 0, len(cs))
	for _, c := range cs {
		out = append(out, campaignOut(c))
	}
	return out
}

func coverageOut(rows []gtmsvc.CoverageRow) []*pb.GtmCoverageRow {
	out := make([]*pb.GtmCoverageRow, 0, len(rows))
	for _, r := range rows {
		counts := make([]*pb.GtmItemStatusCount, 0, len(r.Counts))
		for _, c := range r.Counts {
			counts = append(counts, &pb.GtmItemStatusCount{
				Status: enumOut[pb.GtmItemStatus](c.Status, pb.GtmItemStatus_value),
				Count:  count32(c.Count),
			})
		}
		out = append(out, &pb.GtmCoverageRow{
			ChannelId: r.ChannelID,
			Format:    enumOut[pb.GtmItemFormat](r.Format, pb.GtmItemFormat_value),
			Planned:   r.Planned,
			Counts:    counts,
			Total:     count32(r.Total),
		})
	}
	return out
}

func itemOut(i gtmsvc.Item) *pb.GtmItem {
	return &pb.GtmItem{
		Id:             i.ID,
		OrgId:          i.OrgID,
		CampaignId:     i.CampaignID,
		IdeaId:         i.IdeaID,
		Format:         enumOut[pb.GtmItemFormat](i.Format, pb.GtmItemFormat_value),
		ChannelId:      i.ChannelID,
		Title:          i.Title,
		BodyMd:         i.BodyMD,
		Status:         enumOut[pb.GtmItemStatus](i.Status, pb.GtmItemStatus_value),
		ScheduledAt:    optTimeOut(i.ScheduledAt),
		EmailTo:        i.EmailTo,
		EmailSubject:   i.EmailSubject,
		GmailMessageId: i.GmailMessageID,
		SentAt:         optTimeOut(i.SentAt),
		SentBy:         i.SentBy,
		ApprovalHash:   i.ApprovalHash,
		Revision:       i.Revision,
		CreatedAt:      timeOut(i.CreatedAt),
		UpdatedAt:      timeOut(i.UpdatedAt),
		PublishedUrl:   i.PublishedURL,
	}
}

func itemsOut(is []gtmsvc.Item) []*pb.GtmItem {
	out := make([]*pb.GtmItem, 0, len(is))
	for _, i := range is {
		out = append(out, itemOut(i))
	}
	return out
}

func positioningOut(p gtmsvc.Positioning) *pb.GtmPositioning {
	return &pb.GtmPositioning{
		OrgId:         p.OrgID,
		PositioningMd: p.PositioningMD,
		Audiences:     p.Audiences,
		UpdatedBy:     p.UpdatedBy,
		Revision:      p.Revision,
		UpdatedAt:     timeOut(p.UpdatedAt),

		BusinessType:   enumOut[pb.GtmBusinessType](p.BusinessType, pb.GtmBusinessType_value),
		BusinessModels: enumsOut[pb.GtmBusinessModel](p.BusinessModels, pb.GtmBusinessModel_value),
	}
}

func claimOut(c gtmsvc.Claim) *pb.GtmClaim {
	return &pb.GtmClaim{
		Id:                 c.ID,
		OrgId:              c.OrgID,
		Text:               c.Text,
		TextNorm:           c.TextNorm,
		Category:           enumOut[pb.GtmClaimCategory](c.Category, pb.GtmClaimCategory_value),
		Status:             enumOut[pb.GtmClaimStatus](c.Status, pb.GtmClaimStatus_value),
		SourceUrl:          c.SourceURL,
		SourceKind:         enumOut[pb.GtmClaimSourceKind](c.SourceKind, pb.GtmClaimSourceKind_value),
		VerifiedAt:         optTimeOut(c.VerifiedAt),
		VerifiedBy:         c.VerifiedBy,
		ExpiresOrRecheckAt: optTimeOut(c.ExpiresOrRecheckAt),
		Notes:              c.Notes,
		Revision:           c.Revision,
		CreatedAt:          timeOut(c.CreatedAt),
		UpdatedAt:          timeOut(c.UpdatedAt),
	}
}

func claimsOut(cs []gtmsvc.Claim) []*pb.GtmClaim {
	out := make([]*pb.GtmClaim, 0, len(cs))
	for _, c := range cs {
		out = append(out, claimOut(c))
	}
	return out
}

func findingsOut(fs []gtmsvc.ClaimFinding) []*pb.GtmClaimFinding {
	out := make([]*pb.GtmClaimFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, &pb.GtmClaimFinding{
			Kind:        enumOut[pb.GtmClaimFindingKind](f.Kind, pb.GtmClaimFindingKind_value),
			MentionKind: f.MentionKind,
			Mention:     f.Mention,
			Context:     f.Context,
			ClaimId:     f.ClaimID,
			ClaimText:   f.ClaimText,
			Reason:      f.Reason,
			Blocking:    f.Blocking,
		})
	}
	return out
}
