// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"math"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
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

// libraryEntryIn is an imported entry. Its import stamps (source_revision,
// imported_at, imported_by) are the import's, so they are not read.
func libraryEntryIn(e *pb.GtmStrategyLibraryEntry) gtmsvc.LibraryEntry {
	steps := make([]gtmsvc.LibraryStep, 0, len(e.GetSteps()))
	for _, s := range e.GetSteps() {
		steps = append(steps, gtmsvc.LibraryStep{Who: s.GetWho(), Title: s.GetTitle()})
	}
	var extra map[string]any
	if e.GetExtra() != nil {
		extra = e.GetExtra().AsMap()
	}
	return gtmsvc.LibraryEntry{
		Key:           e.GetKey(),
		Category:      enumIn[gtmsvc.StrategyCategory](e.GetCategory()),
		Depth:         enumIn[gtmsvc.LibraryDepth](e.GetDepth()),
		Name:          e.GetName(),
		SummaryMD:     e.GetSummaryMd(),
		Icon:          e.GetIcon(),
		Stage:         e.GetStage(),
		Cost:          e.GetCost(),
		CostNote:      e.GetCostNote(),
		Difficulty:    e.GetDifficulty(),
		Level:         e.GetLevel(),
		Objective:     e.GetObjective(),
		NorthStar:     e.GetNorthStar(),
		HypothesisMD:  e.GetHypothesisMd(),
		Hours:         e.GetHours(),
		SignalDays:    e.GetSignalDays(),
		WindowDays:    e.GetWindowDays(),
		LaunchDays:    e.GetLaunchDays(),
		ExpectedDelta: e.GetExpectedDelta(),
		Lifecycle:     e.GetLifecycle(),
		ChannelTypes:  e.GetChannelTypes(),
		Integrations:  e.GetIntegrations(),
		Fits:          e.GetFits(),
		Prerequisites: e.GetPrerequisites(),
		KPIs:          e.GetKpis(),
		Tactics:       e.GetTactics(),
		Automates:     e.GetAutomates(),
		Experiments:   e.GetExperiments(),
		StopWhen:      e.GetStopWhen(),
		Steps:         steps,
		Extra:         extra,
	}
}

// libraryEntryOut is an entry as stored. Its extra came from JSON, so it is
// always a valid Struct; one that somehow is not is left out rather than
// failing the read.
func libraryEntryOut(e gtmsvc.LibraryEntry) *pb.GtmStrategyLibraryEntry {
	steps := make([]*pb.GtmLibraryStep, 0, len(e.Steps))
	for _, s := range e.Steps {
		steps = append(steps, &pb.GtmLibraryStep{Who: s.Who, Title: s.Title})
	}
	var extra *structpb.Struct
	if len(e.Extra) > 0 {
		if s, err := structpb.NewStruct(e.Extra); err == nil {
			extra = s
		}
	}
	return &pb.GtmStrategyLibraryEntry{
		Key:            e.Key,
		Category:       enumOut[pb.GtmStrategyCategory](e.Category, pb.GtmStrategyCategory_value),
		Depth:          enumOut[pb.GtmLibraryDepth](e.Depth, pb.GtmLibraryDepth_value),
		Name:           e.Name,
		SummaryMd:      e.SummaryMD,
		Icon:           e.Icon,
		Stage:          e.Stage,
		Cost:           e.Cost,
		CostNote:       e.CostNote,
		Difficulty:     e.Difficulty,
		Level:          e.Level,
		Objective:      e.Objective,
		NorthStar:      e.NorthStar,
		HypothesisMd:   e.HypothesisMD,
		Hours:          e.Hours,
		SignalDays:     e.SignalDays,
		WindowDays:     e.WindowDays,
		LaunchDays:     e.LaunchDays,
		ExpectedDelta:  e.ExpectedDelta,
		Lifecycle:      e.Lifecycle,
		ChannelTypes:   e.ChannelTypes,
		Integrations:   e.Integrations,
		Fits:           e.Fits,
		Prerequisites:  e.Prerequisites,
		Kpis:           e.KPIs,
		Tactics:        e.Tactics,
		Automates:      e.Automates,
		Experiments:    e.Experiments,
		StopWhen:       e.StopWhen,
		Steps:          steps,
		Extra:          extra,
		SourceRevision: e.SourceRevision,
		ImportedAt:     timeOut(e.ImportedAt),
		ImportedBy:     e.ImportedBy,
	}
}

func libraryEntriesOut(es []gtmsvc.LibraryEntry) []*pb.GtmStrategyLibraryEntry {
	out := make([]*pb.GtmStrategyLibraryEntry, 0, len(es))
	for _, e := range es {
		out = append(out, libraryEntryOut(e))
	}
	return out
}

// libraryImportsOut is the import audit of orgID (the domain's rows carry
// no org: they are all the actor's).
func libraryImportsOut(orgID string, is []gtmsvc.LibraryImport) []*pb.GtmLibraryImport {
	out := make([]*pb.GtmLibraryImport, 0, len(is))
	for _, i := range is {
		out = append(out, &pb.GtmLibraryImport{
			Id:             i.ID,
			OrgId:          orgID,
			ActorUserId:    i.ActorUserID,
			CredentialKind: enumOut[pb.GtmCredentialKind](i.CredentialKind, pb.GtmCredentialKind_value),
			SourceRevision: i.SourceRevision,
			Created:        count32(i.Created),
			Updated:        count32(i.Updated),
			Unchanged:      count32(i.Unchanged),
			Pruned:         count32(i.Pruned),
			PruneRequestId: i.PruneRequestID,
			CreatedAt:      timeOut(i.CreatedAt),
		})
	}
	return out
}

// pruneRequestOut is a prune request of orgID; an undecided one has no
// decided_at.
func pruneRequestOut(orgID string, r gtmsvc.LibraryPruneRequest) *pb.GtmLibraryPruneRequest {
	return &pb.GtmLibraryPruneRequest{
		Id:             r.ID,
		OrgId:          orgID,
		RequestedBy:    r.RequestedBy,
		CredentialKind: enumOut[pb.GtmCredentialKind](r.CredentialKind, pb.GtmCredentialKind_value),
		SourceRevision: r.SourceRevision,
		RemoveKeys:     r.RemoveKeys,
		Status:         enumOut[pb.GtmPruneRequestStatus](r.Status, pb.GtmPruneRequestStatus_value),
		DecidedBy:      r.DecidedBy,
		DecidedAt:      timeOut(r.DecidedAt),
		CreatedAt:      timeOut(r.CreatedAt),
		ExpiresAt:      timeOut(r.ExpiresAt),
	}
}

func pruneRequestsOut(orgID string, rs []gtmsvc.LibraryPruneRequest) []*pb.GtmLibraryPruneRequest {
	out := make([]*pb.GtmLibraryPruneRequest, 0, len(rs))
	for _, r := range rs {
		out = append(out, pruneRequestOut(orgID, r))
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
