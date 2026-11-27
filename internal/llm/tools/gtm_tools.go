// Copyright (c) 2026 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The GTM tools are an agent's hands on an org's go-to-market workspace
// (internal/gtm): ideas, strategies and the idea bank, campaigns, the
// calendar of items, channels, positioning, claims and product analytics.
//
// Every tool resolves the calling chat (resolveRunCaller) and acts as
// gtm.Actor{Kind: Agent} for the chat's user, in the chat's org. Access,
// validation and the agent's limits are gtm.Service's, decided once for the
// agent and the web app alike: an agent proposes ideas, drafts strategies,
// campaigns and items, adds manual channels, and never approves, publishes,
// verifies a claim or touches an integration. A refusal at the GTM gate is
// the bare gtm.ErrNotFound, which these tools word exactly as a missing row.
// The tools themselves are feature-gated (feature_gated.go), so to a caller
// not entitled to GTM they do not exist at all.
//
// They are server-placed and need no machine. The only one that leaves
// Reliant is send_gtm_email, which needs the owner's approval card for every
// send and is refused in an unattended run.

const (
	ToolListGtmIdeas           = names.ToolListGtmIdeas
	ToolSaveGtmIdea            = names.ToolSaveGtmIdea
	ToolListGtmStrategies      = names.ToolListGtmStrategies
	ToolSaveGtmStrategy        = names.ToolSaveGtmStrategy
	ToolListGtmStrategyLibrary = names.ToolListGtmStrategyLibrary
	ToolListGtmCampaigns       = names.ToolListGtmCampaigns
	ToolSaveGtmCampaign        = names.ToolSaveGtmCampaign
	ToolListGtmCalendar        = names.ToolListGtmCalendar
	ToolSaveGtmItem            = names.ToolSaveGtmItem
	ToolListGtmChannels        = names.ToolListGtmChannels
	ToolSaveGtmChannel         = names.ToolSaveGtmChannel
	ToolGetGtmPositioning      = names.ToolGetGtmPositioning
	ToolCheckGtmClaims         = names.ToolCheckGtmClaims
	ToolReadProductAnalytics   = names.ToolReadProductAnalytics
	ToolAttachGtmAsset         = names.ToolAttachGtmAsset
	ToolSendGtmEmail           = names.ToolSendGtmEmail
)

// GTMService is the part of gtm.Service the GTM tools call; gtm.Service
// satisfies it. Optional on the factory: nil makes every GTM tool report
// that GTM is unavailable here.
type GTMService interface {
	ListIdeas(ctx context.Context, actor gtm.Actor, in gtm.ListIdeasInput) (gtm.ListIdeasResult, error)
	GetIdea(ctx context.Context, actor gtm.Actor, in gtm.GetIdeaInput) (gtm.Idea, error)
	CreateIdea(ctx context.Context, actor gtm.Actor, in gtm.CreateIdeaInput) (gtm.Idea, error)
	UpdateIdea(ctx context.Context, actor gtm.Actor, in gtm.UpdateIdeaInput) (gtm.Idea, error)

	ListStrategies(ctx context.Context, actor gtm.Actor, in gtm.ListStrategiesInput) (gtm.ListStrategiesResult, error)
	GetStrategy(ctx context.Context, actor gtm.Actor, in gtm.GetStrategyInput) (gtm.StrategyDetail, error)
	CreateStrategy(ctx context.Context, actor gtm.Actor, in gtm.CreateStrategyInput) (gtm.Strategy, error)
	UpdateStrategy(ctx context.Context, actor gtm.Actor, in gtm.UpdateStrategyInput) (gtm.Strategy, error)
	UseLibraryStrategy(ctx context.Context, actor gtm.Actor, in gtm.UseLibraryStrategyInput) (gtm.UseLibraryStrategyResult, error)
	ListStrategyLibrary(ctx context.Context, actor gtm.Actor, in gtm.ListStrategyLibraryInput) (gtm.ListStrategyLibraryResult, error)
	GetStrategyLibraryEntry(ctx context.Context, actor gtm.Actor, in gtm.GetStrategyLibraryEntryInput) (gtm.LibraryEntry, error)

	ListCampaigns(ctx context.Context, actor gtm.Actor, in gtm.ListCampaignsInput) (gtm.ListCampaignsResult, error)
	GetCampaign(ctx context.Context, actor gtm.Actor, in gtm.GetCampaignInput) (gtm.CampaignDetail, error)
	CreateCampaign(ctx context.Context, actor gtm.Actor, in gtm.CreateCampaignInput) (gtm.Campaign, error)
	UpdateCampaign(ctx context.Context, actor gtm.Actor, in gtm.UpdateCampaignInput) (gtm.Campaign, error)

	ListItems(ctx context.Context, actor gtm.Actor, in gtm.ListItemsInput) (gtm.ListItemsResult, error)
	GetItem(ctx context.Context, actor gtm.Actor, in gtm.GetItemInput) (gtm.Item, error)
	CreateItem(ctx context.Context, actor gtm.Actor, in gtm.CreateItemInput) (gtm.Item, error)
	UpdateItem(ctx context.Context, actor gtm.Actor, in gtm.UpdateItemInput) (gtm.Item, error)

	ListChannels(ctx context.Context, actor gtm.Actor, in gtm.ListChannelsInput) (gtm.ListChannelsResult, error)
	GetChannel(ctx context.Context, actor gtm.Actor, in gtm.GetChannelInput) (gtm.Channel, error)
	CreateChannel(ctx context.Context, actor gtm.Actor, in gtm.CreateChannelInput) (gtm.Channel, error)
	UpdateChannel(ctx context.Context, actor gtm.Actor, in gtm.UpdateChannelInput) (gtm.Channel, error)

	GetPositioning(ctx context.Context, actor gtm.Actor) (gtm.Positioning, error)
	CheckClaims(ctx context.Context, actor gtm.Actor, in gtm.CheckClaimsInput) (gtm.CheckClaimsResult, error)
	ReadProductAnalytics(ctx context.Context, actor gtm.Actor, q gtm.AnalyticsQuery) (gtm.AnalyticsResult, error)
	AttachFromAttachment(ctx context.Context, actor gtm.Actor, in gtm.AttachFromAttachmentInput) (gtm.Asset, error)
}

// GTMEmailSender is send_gtm_email's logic: internal/gtm/email's *Approver,
// which this package cannot import (that package reaches it through the
// Gmail integration). Optional on the factory: nil makes send_gtm_email
// refuse every call.
type GTMEmailSender interface {
	// PrepareSend is the approval card for sending itemID at
	// expectedRevision: its title and the hash an approval binds, opaque
	// here. Unattended, it refuses before reading anything.
	PrepareSend(ctx context.Context, actor gtm.Actor, itemID string, expectedRevision int32, unattended bool) (title, hash string, err error)
	// SendApproved sends the item if the owner approved a card for exactly
	// this call (the runtime's approvals, keyed by run.ToolCallID, on ctx)
	// whose hash still matches the item as it is now.
	SendApproved(ctx context.Context, actor gtm.Actor, itemID string, expectedRevision int32, run gtm.EmailRun) (gtm.Item, error)
}

// gtmAgentLimits ends every GTM tool's description: what an agent cannot do,
// so it says so instead of trying.
const gtmAgentLimits = " You can't approve or publish anything, mark an item scheduled or published, verify a claim, change a strategy's status, or connect an integration or set one up on a channel: those are a person's, in the GTM pages of the Reliant web app."

const gtmUnavailable = "GTM is not available on this server."

// List sizes the tools return: compact, with ids and revisions to follow up.
const (
	gtmDefaultPage = 20
	gtmMaxPage     = 50
	// gtmDetailCap caps the lists inside one record (a campaign's ideas and
	// items, a strategy's campaigns, a check's findings).
	gtmDetailCap = 50
	// gtmPreviewRunes is how much of a body a list shows.
	gtmPreviewRunes = 240
)

// =============================================================================
// Shared plumbing
// =============================================================================

// gtmDeps is what every GTM tool holds: the chat store the caller is
// resolved from, and the service.
type gtmDeps struct {
	repo runCallerStore
	svc  GTMService
}

// gtmActor is the agent actor for the calling chat: its user, in its org.
// A chat with no org gets an actor the service refuses as not found.
func gtmActor(ctx *rctx.ToolContext, repo runCallerStore) (gtm.Actor, *ToolResponse) {
	caller, failure := resolveRunCaller(ctx, repo)
	if failure != nil {
		return gtm.Actor{}, failure
	}
	return gtm.Actor{Kind: gtm.ActorAgent, UserID: caller.userID, OrgID: ptr.From(caller.chat.OrgID), ChatID: caller.chat.ID}, nil
}

// gtmTool is one GTM tool: its name, description and the call it makes.
// Every GTM tool but send_gtm_email is one of these.
type gtmTool[P any] struct {
	gtmDeps
	name        string
	description string
	run         func(ctx context.Context, svc GTMService, actor gtm.Actor, p P) ToolResponse
}

func newGTMTool[P any](d gtmDeps, name, description string, run func(context.Context, GTMService, gtm.Actor, P) ToolResponse) Tool {
	return NewToolWrapper[P, ToolResponse](&gtmTool[P]{gtmDeps: d, name: name, description: description, run: run})
}

func (t *gtmTool[P]) Name() string        { return t.name }
func (t *gtmTool[P]) Description() string { return t.description + gtmAgentLimits }

// RequiresPermission is false: a GTM tool reads the org's workspace or saves
// a draft in it, and nothing it saves leaves Reliant or goes live.
func (t *gtmTool[P]) RequiresPermission(P) (bool, error) { return false, nil }

func (t *gtmTool[P]) Execute(ctx *rctx.ToolContext, p P) (ToolResponse, error) {
	actor, failure := gtmActor(ctx, t.repo)
	if failure != nil {
		return *failure, nil
	}
	if t.svc == nil {
		return NewTextErrorResponse(gtmUnavailable), nil
	}
	return t.run(ctx.Context, t.svc, actor, p), nil
}

// gtmJSON is a result for the model: compact JSON.
func gtmJSON(v any) ToolResponse {
	b, err := json.Marshal(v)
	if err != nil {
		return NewTextErrorResponse("Could not encode the result: " + err.Error())
	}
	return NewTextResponse(string(b))
}

// gtmFailure words a service error for the model. notFound is what a
// missing row reads as, and a refusal at the GTM gate is the same bare
// gtm.ErrNotFound, so it reads exactly the same: the tool cannot be used to
// tell "no GTM" from "no such row". A write appends that nothing was saved.
func gtmFailure(err error, notFound string, write bool) ToolResponse {
	msg := gtmErrorText(err, notFound)
	if write {
		msg = strings.TrimSuffix(msg, ".") + ". Nothing was saved."
	}
	return NewTextErrorResponse(msg)
}

func gtmErrorText(err error, notFound string) string {
	var (
		link       *gtm.LinkError
		invalid    *gtm.ValidationError
		conflict   *gtm.RevisionConflictError
		personOnly *gtm.PersonOnlyError
	)
	switch {
	case errors.As(err, &link):
		// Only an entitled caller gets this far: the gate is checked first.
		return link.Error() + "; use an id a list_gtm_* tool returned"
	case errors.Is(err, gtm.ErrNotFound):
		return notFound
	case errors.As(err, &invalid):
		return "Invalid " + invalid.Field + ": " + invalid.Reason
	case errors.As(err, &conflict):
		return fmt.Sprintf("%s changed since you read it: it is at revision %d, not %d. Read it again, and if your change still applies send it with expected_revision %d", conflict.ID, conflict.Current, conflict.Expected, conflict.Current)
	case errors.As(err, &personOnly):
		return "Only a person can " + personOnly.Action + ", in the GTM pages of the Reliant web app; tell the user what you would like them to do"
	}
	// Every other domain error carries a fixed message written for the
	// caller; anything else is reduced to what a client may read.
	return svcerr.ToConnect(err).Message()
}

func gtmNotFound(entity, id, lister string) string {
	if id == "" {
		return "Not found."
	}
	return fmt.Sprintf("No GTM %s %q. %s lists them; never guess ids.", entity, id, lister)
}

// Enum values travel as their short names ("DRAFT"); the service stores
// the proto value names ("GTM_ITEM_STATUS_DRAFT").
const (
	gtmIdeaKind       = "GTM_IDEA_KIND_"
	gtmIdeaStatus     = "GTM_IDEA_STATUS_"
	gtmIdeaSource     = "GTM_IDEA_SOURCE_"
	gtmCampaignStatus = "GTM_CAMPAIGN_STATUS_"
	gtmItemStatus     = "GTM_ITEM_STATUS_"
	gtmItemFormat     = "GTM_ITEM_FORMAT_"
	gtmStrategyStatus = "GTM_STRATEGY_STATUS_"
	gtmCategory       = "GTM_STRATEGY_CATEGORY_"
	gtmChannelMode    = "GTM_CHANNEL_MODE_"
	gtmChannelStatus  = "GTM_CHANNEL_STATUS_"
	gtmChannelSource  = "GTM_CHANNEL_SOURCE_"
	gtmConnection     = "GTM_CONNECTION_STATE_"
	gtmFindingKind    = "GTM_CLAIM_FINDING_KIND_"
	gtmAssetStatus    = "GTM_ASSET_STATUS_"
	gtmAssetSource    = "GTM_ASSET_SOURCE_"
)

// gtmLong is v as the service's enum value: "" stays "" (unset), and a
// value already prefixed is kept.
func gtmLong(prefix, v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" || strings.HasPrefix(v, prefix) {
		return v
	}
	return prefix + v
}

func gtmShort(prefix, v string) string { return strings.TrimPrefix(v, prefix) }

func gtmShorts[T ~string](prefix string, vs []T) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, gtmShort(prefix, string(v)))
	}
	return out
}

func gtmFormats(vs []string) []gtm.ItemFormat {
	out := make([]gtm.ItemFormat, 0, len(vs))
	for _, v := range vs {
		out = append(out, gtm.ItemFormat(gtmLong(gtmItemFormat, v)))
	}
	return out
}

// gtmTime reads a time an agent wrote: RFC 3339, or a YYYY-MM-DD date
// (midnight UTC). "" is nil.
func gtmTime(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, fmt.Errorf("%s %q is not an RFC 3339 time or a YYYY-MM-DD date", field, s)
}

func gtmTimeOut(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// gtmPreview is a list's view of a body: its first gtmPreviewRunes runes.
func gtmPreview(s string) (string, bool) {
	if utf8.RuneCountInString(s) <= gtmPreviewRunes {
		return s, false
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:gtmPreviewRunes])) + "…", true
}

// gtmPage is a tool page size as the service's: 0 is the tools' default,
// above gtmMaxPage is capped.
func gtmPage(n int) int {
	switch {
	case n <= 0:
		return gtmDefaultPage
	case n > gtmMaxPage:
		return gtmMaxPage
	}
	return n
}

func gtmIDList(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func gtmInvalid(msg string) ToolResponse { return NewTextErrorResponse(msg + ". Nothing was saved.") }

// =============================================================================
// Views: what the model reads
// =============================================================================

type gtmEvidenceView struct {
	Kind      string `json:"kind,omitempty"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	SummaryMD string `json:"summary_md,omitempty"`
}

type gtmIdeaView struct {
	ID            string             `json:"id"`
	Kind          string             `json:"kind"`
	Title         string             `json:"title"`
	BodyMD        string             `json:"body_md,omitempty"`
	BodyTruncated bool               `json:"body_truncated,omitempty"`
	Status        string             `json:"status"`
	Source        string             `json:"source"`
	CampaignID    string             `json:"campaign_id,omitempty"`
	Evidence      []gtmEvidenceView  `json:"evidence,omitempty"`
	LibraryKey    string             `json:"library_key,omitempty"`
	TailorAnswers []gtm.TailorAnswer `json:"tailor_answers,omitempty"`
	// SuggestedWorkflowMD is on a full read only.
	SuggestedWorkflowMD string `json:"suggested_workflow_md,omitempty"`
	Revision            int32  `json:"revision"`
	UpdatedAt           string `json:"updated_at"`
}

func gtmIdea(i gtm.Idea, full bool) gtmIdeaView {
	v := gtmIdeaView{
		ID: i.ID, Kind: gtmShort(gtmIdeaKind, string(i.Kind)), Title: i.Title, BodyMD: i.BodyMD,
		Status: gtmShort(gtmIdeaStatus, string(i.Status)), Source: gtmShort(gtmIdeaSource, string(i.Source)),
		CampaignID: i.CampaignID, LibraryKey: i.LibraryKey, Revision: i.Revision, UpdatedAt: gtmTimeOut(&i.UpdatedAt),
	}
	if !full {
		v.BodyMD, v.BodyTruncated = gtmPreview(i.BodyMD)
		return v
	}
	for _, e := range i.Evidence {
		v.Evidence = append(v.Evidence, gtmEvidenceView{Kind: e.Kind, Title: e.Title, URL: e.URL, SummaryMD: e.SummaryMD})
	}
	v.TailorAnswers, v.SuggestedWorkflowMD = i.TailorAnswers, i.SuggestedWorkflowMD
	return v
}

type gtmStrategyView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	GoalMD       string `json:"goal_md,omitempty"`
	TargetMetric string `json:"target_metric,omitempty"`
	PeriodStart  string `json:"period_start,omitempty"`
	PeriodEnd    string `json:"period_end,omitempty"`
	Status       string `json:"status"`
	Category     string `json:"category,omitempty"`
	LibraryKey   string `json:"library_key,omitempty"`
	Revision     int32  `json:"revision"`
}

func gtmStrategy(s gtm.Strategy) gtmStrategyView {
	return gtmStrategyView{
		ID: s.ID, Name: s.Name, GoalMD: s.GoalMD, TargetMetric: s.TargetMetric, PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd,
		Status: gtmShort(gtmStrategyStatus, string(s.Status)), Category: gtmShort(gtmCategory, string(s.Category)),
		LibraryKey: s.LibraryKey, Revision: s.Revision,
	}
}

type gtmCampaignView struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Goal              string   `json:"goal,omitempty"`
	Status            string   `json:"status"`
	StrategyID        string   `json:"strategy_id"`
	PeriodStart       string   `json:"period_start,omitempty"`
	PeriodEnd         string   `json:"period_end,omitempty"`
	PlannedChannelIDs []string `json:"planned_channel_ids,omitempty"`
	PlannedFormats    []string `json:"planned_formats,omitempty"`
	Revision          int32    `json:"revision"`
}

func gtmCampaign(c gtm.Campaign) gtmCampaignView {
	return gtmCampaignView{
		ID: c.ID, Name: c.Name, Goal: c.Goal, Status: gtmShort(gtmCampaignStatus, string(c.Status)), StrategyID: c.StrategyID,
		PeriodStart: gtmTimeOut(c.PeriodStart), PeriodEnd: gtmTimeOut(c.PeriodEnd),
		PlannedChannelIDs: c.PlannedChannelIDs, PlannedFormats: gtmShorts(gtmItemFormat, c.PlannedFormats), Revision: c.Revision,
	}
}

type gtmItemView struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Format        string   `json:"format"`
	ChannelID     string   `json:"channel_id,omitempty"`
	Status        string   `json:"status"`
	ScheduledAt   string   `json:"scheduled_at,omitempty"`
	CampaignID    string   `json:"campaign_id,omitempty"`
	IdeaID        string   `json:"idea_id,omitempty"`
	BodyMD        string   `json:"body_md,omitempty"`
	BodyTruncated bool     `json:"body_truncated,omitempty"`
	EmailTo       []string `json:"email_to,omitempty"`
	EmailSubject  string   `json:"email_subject,omitempty"`
	PublishedURL  string   `json:"published_url,omitempty"`
	SentAt        string   `json:"sent_at,omitempty"`
	Revision      int32    `json:"revision"`
}

func gtmItem(i gtm.Item, full bool) gtmItemView {
	v := gtmItemView{
		ID: i.ID, Title: i.Title, Format: gtmShort(gtmItemFormat, string(i.Format)), ChannelID: i.ChannelID,
		Status: gtmShort(gtmItemStatus, string(i.Status)), ScheduledAt: gtmTimeOut(i.ScheduledAt),
		CampaignID: i.CampaignID, IdeaID: i.IdeaID, BodyMD: i.BodyMD, EmailTo: i.EmailTo, EmailSubject: i.EmailSubject,
		PublishedURL: i.PublishedURL, SentAt: gtmTimeOut(i.SentAt), Revision: i.Revision,
	}
	if !full {
		v.BodyMD, v.BodyTruncated = gtmPreview(i.BodyMD)
	}
	return v
}

// gtmMaxChannels caps list_gtm_channels (the service reads channels
// unpaged, up to gtm.MaxChannels).
const gtmMaxChannels = gtmMaxPage

type gtmChannelView struct {
	ID                    string   `json:"id"`
	Key                   string   `json:"key"`
	Name                  string   `json:"name"`
	Mode                  string   `json:"mode"`
	IntegrationKind       string   `json:"integration_kind,omitempty"`
	ConnectionState       string   `json:"connection_state,omitempty"`
	ManualInstructionsMD  string   `json:"manual_instructions_md,omitempty"`
	InstructionsTruncated bool     `json:"manual_instructions_truncated,omitempty"`
	ChannelTypes          []string `json:"channel_types,omitempty"`
	Status                string   `json:"status"`
	Source                string   `json:"source,omitempty"`
	Revision              int32    `json:"revision"`
}

func gtmChannel(c gtm.Channel, full bool) gtmChannelView {
	v := gtmChannelView{
		ID: c.ID, Key: c.Key, Name: c.Name, Mode: gtmShort(gtmChannelMode, string(c.Mode)), IntegrationKind: c.IntegrationKind,
		ConnectionState: gtmShort(gtmConnection, string(c.ConnectionState)), ManualInstructionsMD: c.ManualInstructionsMD,
		ChannelTypes: c.ChannelTypes, Status: gtmShort(gtmChannelStatus, string(c.Status)),
		Source: gtmShort(gtmChannelSource, string(c.Source)), Revision: c.Revision,
	}
	if !full {
		v.ManualInstructionsMD, v.InstructionsTruncated = gtmPreview(c.ManualInstructionsMD)
	}
	return v
}

// =============================================================================
// Ideas
// =============================================================================

type ListGtmIdeasParams struct {
	ID         string `json:"id,omitempty" jsonschema:"description=One idea's id: returns it whole (body and evidence). Omit to list."`
	Status     string `json:"status,omitempty" jsonschema:"enum=PROPOSED,enum=ACCEPTED,enum=DONE,enum=REJECTED,description=Only ideas in this status."`
	Kind       string `json:"kind,omitempty" jsonschema:"enum=PRODUCT,enum=GTM,enum=CONTENT,description=Only ideas of this kind."`
	CampaignID string `json:"campaign_id,omitempty" jsonschema:"description=Only ideas linked to this campaign."`
	PageSize   int    `json:"page_size,omitempty" jsonschema:"description=Ideas per page: default 20; at most 50."`
	PageToken  string `json:"page_token,omitempty" jsonschema:"description=next_page_token from the previous page."`
}

const listGtmIdeasDescription = "List the org's GTM ideas (things to build, go-to-market moves, content to write), newest first, or read one whole with id. A list shows each idea's id, kind, title, the start of its body, status, source (AGENT or HUMAN), campaign and revision; save_gtm_idea needs the id and revision to edit one."

func listGtmIdeas(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmIdeasParams) ToolResponse {
	if id := strings.TrimSpace(p.ID); id != "" {
		idea, err := svc.GetIdea(ctx, actor, gtm.GetIdeaInput{ID: id})
		if err != nil {
			return gtmFailure(err, gtmNotFound("idea", id, ToolListGtmIdeas), false)
		}
		return gtmJSON(gtmIdea(idea, true))
	}
	in := gtm.ListIdeasInput{
		Kind: gtm.IdeaKind(gtmLong(gtmIdeaKind, p.Kind)), Status: gtm.IdeaStatus(gtmLong(gtmIdeaStatus, p.Status)),
		PageSize: gtmPage(p.PageSize), PageToken: p.PageToken,
	}
	if c := strings.TrimSpace(p.CampaignID); c != "" {
		in.CampaignID = &c
	}
	res, err := svc.ListIdeas(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, gtmNotFound("campaign", strings.TrimSpace(p.CampaignID), ToolListGtmCampaigns), false)
	}
	out := struct {
		Ideas         []gtmIdeaView `json:"ideas"`
		NextPageToken string        `json:"next_page_token,omitempty"`
	}{Ideas: []gtmIdeaView{}, NextPageToken: res.NextPageToken}
	for _, i := range res.Ideas {
		out.Ideas = append(out.Ideas, gtmIdea(i, false))
	}
	return gtmJSON(out)
}

type GtmEvidenceParam struct {
	Kind      string `json:"kind,omitempty" jsonschema:"description=What it is: analytics or link or note."`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	SummaryMD string `json:"summary_md,omitempty" jsonschema:"description=What it shows\\, in markdown."`
}

type SaveGtmIdeaParams struct {
	ID                  string              `json:"id,omitempty" jsonschema:"description=The idea to edit. Omit to create one."`
	ExpectedRevision    int32               `json:"expected_revision,omitempty" jsonschema:"description=The revision you read. Required to edit."`
	Kind                string              `json:"kind,omitempty" jsonschema:"enum=PRODUCT,enum=GTM,enum=CONTENT,description=Required to create."`
	Title               *string             `json:"title,omitempty" jsonschema:"description=Required to create."`
	BodyMD              *string             `json:"body_md,omitempty" jsonschema:"description=The idea in markdown: what and why."`
	Evidence            *[]GtmEvidenceParam `json:"evidence,omitempty" jsonschema:"description=What backs it (analytics readouts\\, links\\, notes). On an edit it replaces the whole list."`
	CampaignID          *string             `json:"campaign_id,omitempty" jsonschema:"description=A campaign to link it to. On an edit an empty string unlinks it."`
	LibraryKey          *string             `json:"library_key,omitempty" jsonschema:"description=The idea-bank entry it comes from (list_gtm_strategy_library key). Never guess one. On an edit an empty string clears it."`
	TailorAnswers       *[]GtmTailorAnswer  `json:"tailor_answers,omitempty" jsonschema:"description=That entry's tailor_questions\\, each answered for this business (at most 20; each answer at most 2000 characters). On an edit it replaces the whole list."`
	SuggestedWorkflowMD *string             `json:"suggested_workflow_md,omitempty" jsonschema:"description=How the idea could run as an automation\\, in markdown\\, tool-agnostic: the entry's suggested workflow tailored to this business. A suggestion only: nothing is created from it."`
}

type GtmTailorAnswer struct {
	Question string `json:"question" jsonschema:"required,description=The entry's tailor question\\, as the entry words it."`
	Answer   string `json:"answer" jsonschema:"required,description=The answer for this business."`
}

func gtmTailorAnswers(in *[]GtmTailorAnswer) *[]gtm.TailorAnswer {
	if in == nil {
		return nil
	}
	out := make([]gtm.TailorAnswer, 0, len(*in))
	for _, a := range *in {
		out = append(out, gtm.TailorAnswer{Question: strings.TrimSpace(a.Question), Answer: a.Answer})
	}
	return &out
}

const saveGtmIdeaDescription = "Propose a GTM idea, or edit one you can still edit. A new idea is PROPOSED with source AGENT and stays PROPOSED: accepting, rejecting or finishing one is a person's call, and an agent edits only PROPOSED ideas. An idea drawn from the idea bank (list_gtm_strategy_library) cites its library_key, answers that entry's tailor_questions in tailor_answers, and may carry suggested_workflow_md, the entry's suggested automation tailored to this business (a suggestion: nothing is created from it). Edit with id and expected_revision; fields you omit are left alone. Back an idea with evidence (read_product_analytics readouts, links)."

func saveGtmIdea(ctx context.Context, svc GTMService, actor gtm.Actor, p SaveGtmIdeaParams) ToolResponse {
	var evidence *[]gtm.Evidence
	if p.Evidence != nil {
		ev := make([]gtm.Evidence, 0, len(*p.Evidence))
		for _, e := range *p.Evidence {
			ev = append(ev, gtm.Evidence{Kind: e.Kind, Title: e.Title, URL: e.URL, SummaryMD: e.SummaryMD})
		}
		evidence = &ev
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		if p.Title == nil || p.Kind == "" {
			return gtmInvalid("A new idea needs kind and title")
		}
		in := gtm.CreateIdeaInput{Kind: gtm.IdeaKind(gtmLong(gtmIdeaKind, p.Kind)), Title: *p.Title, BodyMD: ptr.From(p.BodyMD), CampaignID: ptr.From(p.CampaignID),
			LibraryKey: strings.TrimSpace(ptr.From(p.LibraryKey)), SuggestedWorkflowMD: ptr.From(p.SuggestedWorkflowMD)}
		if evidence != nil {
			in.Evidence = *evidence
		}
		if answers := gtmTailorAnswers(p.TailorAnswers); answers != nil {
			in.TailorAnswers = *answers
		}
		idea, err := svc.CreateIdea(ctx, actor, in)
		if err != nil {
			return gtmFailure(err, "Not found.", true)
		}
		return gtmJSON(gtmIdea(idea, true))
	}
	in := gtm.UpdateIdeaInput{ID: id, ExpectedRevision: p.ExpectedRevision, Title: p.Title, BodyMD: p.BodyMD, Evidence: evidence, CampaignID: p.CampaignID,
		LibraryKey: trimPtr(p.LibraryKey), TailorAnswers: gtmTailorAnswers(p.TailorAnswers), SuggestedWorkflowMD: p.SuggestedWorkflowMD}
	if p.Kind != "" {
		k := gtm.IdeaKind(gtmLong(gtmIdeaKind, p.Kind))
		in.Kind = &k
	}
	idea, err := svc.UpdateIdea(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, gtmNotFound("idea", id, ToolListGtmIdeas), true)
	}
	return gtmJSON(gtmIdea(idea, true))
}

// =============================================================================
// Strategies and the idea bank
// =============================================================================

type ListGtmStrategiesParams struct {
	ID        string `json:"id,omitempty" jsonschema:"description=One strategy's id: returns it with its campaigns. Omit to list."`
	Status    string `json:"status,omitempty" jsonschema:"enum=DRAFT,enum=ACTIVE,enum=DONE,enum=ARCHIVED,description=Only strategies in this status."`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"description=Strategies per page: default 20; at most 50."`
	PageToken string `json:"page_token,omitempty" jsonschema:"description=next_page_token from the previous page."`
}

const listGtmStrategiesDescription = "List the org's GTM strategies (a goal with a target metric over a period, pursued by campaigns), newest first, or read one with id together with its campaigns. Every campaign belongs to one strategy."

func listGtmStrategies(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmStrategiesParams) ToolResponse {
	if id := strings.TrimSpace(p.ID); id != "" {
		d, err := svc.GetStrategy(ctx, actor, gtm.GetStrategyInput{ID: id})
		if err != nil {
			return gtmFailure(err, gtmNotFound("strategy", id, ToolListGtmStrategies), false)
		}
		out := struct {
			gtmStrategyView
			Campaigns          []gtmCampaignView `json:"campaigns"`
			CampaignsTruncated bool              `json:"campaigns_truncated,omitempty"`
		}{gtmStrategyView: gtmStrategy(d.Strategy), Campaigns: []gtmCampaignView{}, CampaignsTruncated: d.Truncated}
		for i, c := range d.Campaigns {
			if i == gtmDetailCap {
				out.CampaignsTruncated = true
				break
			}
			out.Campaigns = append(out.Campaigns, gtmCampaign(c))
		}
		return gtmJSON(out)
	}
	res, err := svc.ListStrategies(ctx, actor, gtm.ListStrategiesInput{
		Status: gtm.StrategyStatus(gtmLong(gtmStrategyStatus, p.Status)), PageSize: gtmPage(p.PageSize), PageToken: p.PageToken,
	})
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	out := struct {
		Strategies    []gtmStrategyView `json:"strategies"`
		NextPageToken string            `json:"next_page_token,omitempty"`
	}{Strategies: []gtmStrategyView{}, NextPageToken: res.NextPageToken}
	for _, s := range res.Strategies {
		out.Strategies = append(out.Strategies, gtmStrategy(s))
	}
	return gtmJSON(out)
}

type SaveGtmStrategyParams struct {
	ID                  string  `json:"id,omitempty" jsonschema:"description=The strategy to edit. Omit to create one."`
	ExpectedRevision    int32   `json:"expected_revision,omitempty" jsonschema:"description=The revision you read. Required to edit."`
	FromLibraryKey      string  `json:"from_library_key,omitempty" jsonschema:"description=Create the strategy from this idea-bank entry (list_gtm_strategy_library): name\\, goal\\, target metric and category come from it. Create only; the other fields then do not apply."`
	CreateFirstCampaign bool    `json:"create_first_campaign,omitempty" jsonschema:"description=With from_library_key: also create its first DRAFT campaign\\, planning the org's channels that match the entry."`
	CampaignName        *string `json:"campaign_name,omitempty" jsonschema:"description=With create_first_campaign: the campaign's name. Default: the entry's name."`
	Name                *string `json:"name,omitempty" jsonschema:"description=Required to create (unless from_library_key)."`
	GoalMD              *string `json:"goal_md,omitempty" jsonschema:"description=The goal or north star\\, in markdown."`
	TargetMetric        *string `json:"target_metric,omitempty" jsonschema:"description=What success is measured by\\, e.g. 200 weekly active orgs."`
	PeriodStart         *string `json:"period_start,omitempty" jsonschema:"description=YYYY-MM-DD. On an edit an empty string clears it."`
	PeriodEnd           *string `json:"period_end,omitempty" jsonschema:"description=YYYY-MM-DD\\, not before period_start. On an edit an empty string clears it."`
	Category            *string `json:"category,omitempty" jsonschema:"description=One of PRICING_OFFERS_INCENTIVES REFERRAL_VIRAL_GROWTH PAID_ACQUISITION ORGANIC_CONTENT_SEO OUTBOUND_SALES_PROSPECTING PARTNERSHIPS_DISTRIBUTION LAUNCHES_PR COMMUNITY_EVENTS PRODUCT_LED_GROWTH CONVERSION_OPTIMIZATION RETENTION_EXPANSION TRUST_REPUTATION MARKET_RESEARCH_POSITIONING. On an edit an empty string clears it."`
}

const saveGtmStrategyDescription = "Draft a GTM strategy, or edit a DRAFT one. Every strategy you create is a DRAFT and you edit DRAFT strategies only: a person makes it ACTIVE, DONE or ARCHIVED, and edits it after that. Create from scratch with name (and goal_md, target_metric, period_start, period_end, category), or from a idea-bank entry with from_library_key, optionally with its first campaign. Edit with id and expected_revision; fields you omit are left alone."

func saveGtmStrategy(ctx context.Context, svc GTMService, actor gtm.Actor, p SaveGtmStrategyParams) ToolResponse {
	id, key := strings.TrimSpace(p.ID), strings.TrimSpace(p.FromLibraryKey)
	category := func() *gtm.StrategyCategory {
		if p.Category == nil {
			return nil
		}
		c := gtm.StrategyCategory(gtmLong(gtmCategory, *p.Category))
		return &c
	}
	switch {
	case key != "":
		if id != "" || p.Name != nil || p.GoalMD != nil || p.TargetMetric != nil || p.PeriodStart != nil || p.PeriodEnd != nil || p.Category != nil {
			return gtmInvalid("from_library_key creates a strategy from the entry alone; omit id and the strategy fields, then edit the draft it creates")
		}
		res, err := svc.UseLibraryStrategy(ctx, actor, gtm.UseLibraryStrategyInput{LibraryKey: key, CreateFirstCampaign: p.CreateFirstCampaign, CampaignName: p.CampaignName})
		if err != nil {
			return gtmFailure(err, fmt.Sprintf("No idea-bank entry %q. %s lists them; never guess keys.", key, ToolListGtmStrategyLibrary), true)
		}
		out := struct {
			Strategy gtmStrategyView  `json:"strategy"`
			Campaign *gtmCampaignView `json:"campaign,omitempty"`
		}{Strategy: gtmStrategy(res.Strategy)}
		if res.Campaign != nil {
			c := gtmCampaign(*res.Campaign)
			out.Campaign = &c
		}
		return gtmJSON(out)
	case p.CreateFirstCampaign || p.CampaignName != nil:
		return gtmInvalid("create_first_campaign and campaign_name go with from_library_key; save_gtm_campaign creates a campaign for any strategy")
	case id == "":
		if p.Name == nil {
			return gtmInvalid("A new strategy needs name")
		}
		in := gtm.CreateStrategyInput{Name: *p.Name, GoalMD: ptr.From(p.GoalMD), TargetMetric: ptr.From(p.TargetMetric),
			PeriodStart: ptr.From(p.PeriodStart), PeriodEnd: ptr.From(p.PeriodEnd), Status: gtm.StrategyDraft}
		if c := category(); c != nil {
			in.Category = *c
		}
		s, err := svc.CreateStrategy(ctx, actor, in)
		if err != nil {
			return gtmFailure(err, "Not found.", true)
		}
		return gtmJSON(gtmStrategy(s))
	}
	s, err := svc.UpdateStrategy(ctx, actor, gtm.UpdateStrategyInput{
		ID: id, ExpectedRevision: p.ExpectedRevision, Name: p.Name, GoalMD: p.GoalMD, TargetMetric: p.TargetMetric,
		PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd, Category: category(),
	})
	if err != nil {
		return gtmFailure(err, gtmNotFound("strategy", id, ToolListGtmStrategies), true)
	}
	return gtmJSON(gtmStrategy(s))
}

type ListGtmStrategyLibraryParams struct {
	Key            string   `json:"key,omitempty" jsonschema:"description=One entry's key: returns it whole (why it works\\, suggested workflow\\, KPIs\\, channel types). Omit to list."`
	Diverse        bool     `json:"diverse,omitempty" jsonschema:"description=Return a spread across categories instead of a page: call with diverse=true first when generating ideas."`
	Categories     []string `json:"categories,omitempty" jsonschema:"description=Only these categories (at most 13): PRICING_OFFERS_INCENTIVES REFERRAL_VIRAL_GROWTH PAID_ACQUISITION ORGANIC_CONTENT_SEO OUTBOUND_SALES_PROSPECTING PARTNERSHIPS_DISTRIBUTION LAUNCHES_PR COMMUNITY_EVENTS PRODUCT_LED_GROWTH CONVERSION_OPTIMIZATION RETENTION_EXPANSION TRUST_REPUTATION MARKET_RESEARCH_POSITIONING."`
	BusinessTypes  []string `json:"business_types,omitempty" jsonschema:"description=Only entries that fit one of these business types (or any): B2B_SAAS B2C_APP DEVELOPER_TOOLS ECOMMERCE MARKETPLACE AGENCY_SERVICES CREATOR_MEDIA LOCAL_BUSINESS OPEN_SOURCE HARDWARE."`
	BusinessModels []string `json:"business_models,omitempty" jsonschema:"description=Only entries that fit one of these business models (or any): SUBSCRIPTION USAGE_BASED ONE_TIME FREEMIUM FREE_OSS MARKETPLACE SERVICES AD_SUPPORTED."`
	Stage          string   `json:"stage,omitempty" jsonschema:"enum=pre_launch,enum=launch,enum=early_traction,enum=growth,enum=scale,description=Only entries that fit this stage (or any)."`
	Effort         string   `json:"effort,omitempty" jsonschema:"enum=low,enum=medium,enum=high,description=Only entries of this effort."`
	Cost           string   `json:"cost,omitempty" jsonschema:"enum=free,enum=low,enum=medium,enum=high,description=Only entries of this cost."`
	Query          string   `json:"query,omitempty" jsonschema:"description=Words that must all appear in the name or summary."`
	FitsMyBusiness *bool    `json:"fits_my_business,omitempty" jsonschema:"description=Keep only entries that fit the org's business type and models (get_gtm_positioning). Default true; false browses the whole bank."`
	Limit          int      `json:"limit,omitempty" jsonschema:"description=Entries to return: default 20; at most 50."`
	PageToken      string   `json:"page_token,omitempty" jsonschema:"description=next_page_token from the previous page (not with diverse)."`
}

const listGtmStrategyLibraryDescription = `Browse the GTM idea bank: generic go-to-market plays across 13 categories, filtered by default to the ones that fit the org's business (fits_my_business, from get_gtm_positioning's business type and models). The entries are generic jumping-off points to tailor to this business, not scripts to follow. Read one whole with key.
When generating ideas, call it with diverse=true first, then narrow with categories, business_types, business_models, stage, effort, cost or query. Propose ideas from AT LEAST 4 different categories, including in-person and community ones (meetups, conferences, hackathons, networking) and founder-led, high-touch outreach, unless the business makes them impossible; when you skip one, say why. Each idea you propose (save_gtm_idea) cites the entry's library_key and answers that entry's tailor_questions in tailor_answers, one answer per question, for this business. An entry can also seed a draft strategy (save_gtm_strategy from_library_key). Read-only.`

type gtmLibraryView struct {
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	Category         string   `json:"category"`
	SummaryMD        string   `json:"summary_md,omitempty"`
	SummaryTruncated bool     `json:"summary_truncated,omitempty"`
	BusinessTypes    []string `json:"business_types,omitempty"`
	BusinessModels   []string `json:"business_models,omitempty"`
	Stages           []string `json:"stages,omitempty"`
	Objective        string   `json:"objective,omitempty"`
	Effort           string   `json:"effort,omitempty"`
	Cost             string   `json:"cost,omitempty"`
	SignalDays       int      `json:"signal_days,omitempty"`
	TailorQuestions  []string `json:"tailor_questions,omitempty"`
}

type gtmLibraryEntryView struct {
	gtmLibraryView
	WhyItWorksMD        string   `json:"why_it_works_md,omitempty"`
	SuggestedWorkflowMD string   `json:"suggested_workflow_md,omitempty"`
	ChannelTypes        []string `json:"channel_types,omitempty"`
	KPIs                []string `json:"kpis,omitempty"`
}

const (
	gtmBusinessType  = "GTM_BUSINESS_TYPE_"
	gtmBusinessModel = "GTM_BUSINESS_MODEL_"
)

func gtmLibrary(e gtm.LibraryEntry, full bool) gtmLibraryView {
	v := gtmLibraryView{
		Key: e.Key, Name: e.Name, Category: gtmShort(gtmCategory, string(e.Category)), SummaryMD: e.SummaryMD,
		BusinessTypes: gtmShorts(gtmBusinessType, e.BusinessTypes), BusinessModels: gtmShorts(gtmBusinessModel, e.BusinessModels),
		Stages: e.Stages, Objective: e.Objective, Effort: e.Effort, Cost: e.Cost, SignalDays: e.SignalDays,
		TailorQuestions: e.TailorQuestions,
	}
	if !full {
		v.SummaryMD, v.SummaryTruncated = gtmPreview(e.SummaryMD)
	}
	return v
}

// gtmLongs is vs as the service's enum values, prefixed.
func gtmLongs[T ~string](prefix string, vs []string) []T {
	out := make([]T, 0, len(vs))
	for _, v := range vs {
		if l := gtmLong(prefix, v); l != "" {
			out = append(out, T(l))
		}
	}
	return out
}

func listGtmStrategyLibrary(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmStrategyLibraryParams) ToolResponse {
	if key := strings.TrimSpace(p.Key); key != "" {
		e, err := svc.GetStrategyLibraryEntry(ctx, actor, gtm.GetStrategyLibraryEntryInput{Key: key})
		if err != nil {
			return gtmFailure(err, fmt.Sprintf("No idea-bank entry %q. %s lists them; never guess keys.", key, ToolListGtmStrategyLibrary), false)
		}
		return gtmJSON(gtmLibraryEntryView{
			gtmLibraryView: gtmLibrary(e, true), WhyItWorksMD: e.WhyItWorksMD, SuggestedWorkflowMD: e.SuggestedWorkflowMD,
			ChannelTypes: e.ChannelTypes, KPIs: e.KPIs,
		})
	}
	if p.Diverse && p.PageToken != "" {
		return NewTextErrorResponse("page_token does not apply to diverse: a diverse call returns one spread.")
	}
	fits := p.FitsMyBusiness == nil || *p.FitsMyBusiness
	res, err := svc.ListStrategyLibrary(ctx, actor, gtm.ListStrategyLibraryInput{
		Categories:     gtmLongs[gtm.StrategyCategory](gtmCategory, p.Categories),
		BusinessTypes:  gtmLongs[gtm.BusinessType](gtmBusinessType, p.BusinessTypes),
		BusinessModels: gtmLongs[gtm.BusinessModel](gtmBusinessModel, p.BusinessModels),
		Stage:          strings.ToLower(strings.TrimSpace(p.Stage)), Effort: strings.ToLower(strings.TrimSpace(p.Effort)),
		Cost: strings.ToLower(strings.TrimSpace(p.Cost)), Query: strings.TrimSpace(p.Query),
		FitsMyBusiness: fits, Diverse: p.Diverse, PageSize: gtmPage(p.Limit), PageToken: p.PageToken,
	})
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	out := struct {
		Entries        []gtmLibraryView `json:"entries"`
		FitsMyBusiness bool             `json:"fits_my_business"`
		NextPageToken  string           `json:"next_page_token,omitempty"`
	}{Entries: []gtmLibraryView{}, FitsMyBusiness: fits, NextPageToken: res.NextPageToken}
	for i, e := range res.Entries {
		if i == gtmMaxPage {
			break
		}
		out.Entries = append(out.Entries, gtmLibrary(e, false))
	}
	return gtmJSON(out)
}

// =============================================================================
// Campaigns
// =============================================================================

type ListGtmCampaignsParams struct {
	ID         string `json:"id,omitempty" jsonschema:"description=One campaign's id: returns it with its ideas\\, items and coverage. Omit to list."`
	Status     string `json:"status,omitempty" jsonschema:"enum=DRAFT,enum=ACTIVE,enum=DONE,enum=ARCHIVED,description=Only campaigns in this status."`
	StrategyID string `json:"strategy_id,omitempty" jsonschema:"description=Only this strategy's campaigns."`
	PageSize   int    `json:"page_size,omitempty" jsonschema:"description=Campaigns per page: default 20; at most 50."`
	PageToken  string `json:"page_token,omitempty" jsonschema:"description=next_page_token from the previous page."`
}

const listGtmCampaignsDescription = "List the org's GTM campaigns, newest first, or read one with id: its linked ideas and items, and its coverage, one row per planned or used (channel, format) pair with its items counted by status, so you can see what is planned but not drafted (\"reddit SHORT_POST planned, nothing drafted\")."

type gtmCoverageView struct {
	ChannelID string         `json:"channel_id,omitempty"`
	Format    string         `json:"format,omitempty"`
	Planned   bool           `json:"planned"`
	Counts    map[string]int `json:"counts,omitempty"`
	Total     int            `json:"total"`
}

func listGtmCampaigns(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmCampaignsParams) ToolResponse {
	if id := strings.TrimSpace(p.ID); id != "" {
		d, err := svc.GetCampaign(ctx, actor, gtm.GetCampaignInput{ID: id})
		if err != nil {
			return gtmFailure(err, gtmNotFound("campaign", id, ToolListGtmCampaigns), false)
		}
		out := struct {
			gtmCampaignView
			Ideas             []gtmIdeaView     `json:"ideas"`
			Items             []gtmItemView     `json:"items"`
			Truncated         bool              `json:"truncated,omitempty"`
			Coverage          []gtmCoverageView `json:"coverage"`
			CoverageTruncated bool              `json:"coverage_truncated,omitempty"`
		}{gtmCampaignView: gtmCampaign(d.Campaign), Ideas: []gtmIdeaView{}, Items: []gtmItemView{}, Truncated: d.Truncated, Coverage: []gtmCoverageView{}}
		for i, idea := range d.Ideas {
			if i == gtmDetailCap {
				out.Truncated = true
				break
			}
			out.Ideas = append(out.Ideas, gtmIdea(idea, false))
		}
		for i, it := range d.Items {
			if i == gtmDetailCap {
				out.Truncated = true
				break
			}
			out.Items = append(out.Items, gtmItem(it, false))
		}
		for i, c := range d.Coverage {
			if i == gtmDetailCap {
				out.CoverageTruncated = true
				break
			}
			row := gtmCoverageView{ChannelID: c.ChannelID, Format: gtmShort(gtmItemFormat, string(c.Format)), Planned: c.Planned, Total: c.Total}
			if len(c.Counts) > 0 {
				row.Counts = map[string]int{}
				for _, n := range c.Counts {
					row.Counts[gtmShort(gtmItemStatus, string(n.Status))] = n.Count
				}
			}
			out.Coverage = append(out.Coverage, row)
		}
		return gtmJSON(out)
	}
	in := gtm.ListCampaignsInput{Status: gtm.CampaignStatus(gtmLong(gtmCampaignStatus, p.Status)), PageSize: gtmPage(p.PageSize), PageToken: p.PageToken}
	if s := strings.TrimSpace(p.StrategyID); s != "" {
		in.StrategyID = &s
	}
	res, err := svc.ListCampaigns(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, gtmNotFound("strategy", strings.TrimSpace(p.StrategyID), ToolListGtmStrategies), false)
	}
	out := struct {
		Campaigns     []gtmCampaignView `json:"campaigns"`
		NextPageToken string            `json:"next_page_token,omitempty"`
	}{Campaigns: []gtmCampaignView{}, NextPageToken: res.NextPageToken}
	for _, c := range res.Campaigns {
		out.Campaigns = append(out.Campaigns, gtmCampaign(c))
	}
	return gtmJSON(out)
}

type SaveGtmCampaignParams struct {
	ID                string    `json:"id,omitempty" jsonschema:"description=The campaign to edit. Omit to create one."`
	ExpectedRevision  int32     `json:"expected_revision,omitempty" jsonschema:"description=The revision you read. Required to edit."`
	StrategyID        *string   `json:"strategy_id,omitempty" jsonschema:"description=The strategy it belongs to (list_gtm_strategies). Required to create; on an edit it moves the campaign to that strategy."`
	Name              *string   `json:"name,omitempty" jsonschema:"description=Required to create."`
	Goal              *string   `json:"goal,omitempty"`
	PeriodStart       string    `json:"period_start,omitempty" jsonschema:"description=YYYY-MM-DD or an RFC 3339 time."`
	PeriodEnd         string    `json:"period_end,omitempty" jsonschema:"description=YYYY-MM-DD or an RFC 3339 time."`
	ClearPeriodStart  bool      `json:"clear_period_start,omitempty"`
	ClearPeriodEnd    bool      `json:"clear_period_end,omitempty"`
	PlannedChannelIDs *[]string `json:"planned_channel_ids,omitempty" jsonschema:"description=The channels it plans to use (list_gtm_channels ids). On an edit it replaces the list."`
	PlannedFormats    *[]string `json:"planned_formats,omitempty" jsonschema:"description=The formats it plans: SHORT_POST LONG_POST EMAIL COLD_OUTREACH VIDEO MEDIA_PITCH AD or OTHER. On an edit it replaces the list."`
}

const saveGtmCampaignDescription = "Create a GTM campaign under a strategy, or edit one: name, goal, period, and the channels and formats it plans (which list_gtm_campaigns' coverage checks the items against). A campaign you create is a DRAFT; a person moves it to ACTIVE, DONE or ARCHIVED. Edit with id and expected_revision; fields you omit are left alone. Link ideas and items to it with save_gtm_idea and save_gtm_item campaign_id."

func saveGtmCampaign(ctx context.Context, svc GTMService, actor gtm.Actor, p SaveGtmCampaignParams) ToolResponse {
	start, err := gtmTime("period_start", p.PeriodStart)
	if err != nil {
		return gtmInvalid(err.Error())
	}
	end, err := gtmTime("period_end", p.PeriodEnd)
	if err != nil {
		return gtmInvalid(err.Error())
	}
	var formats *[]gtm.ItemFormat
	if p.PlannedFormats != nil {
		f := gtmFormats(*p.PlannedFormats)
		formats = &f
	}
	var channels *[]string
	if p.PlannedChannelIDs != nil {
		c := gtmIDList(*p.PlannedChannelIDs)
		channels = &c
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		if p.Name == nil || p.StrategyID == nil || strings.TrimSpace(*p.StrategyID) == "" {
			return gtmInvalid("A new campaign needs name and strategy_id (list_gtm_strategies; save_gtm_strategy drafts one)")
		}
		if p.ClearPeriodStart || p.ClearPeriodEnd {
			return gtmInvalid("clear_period_start and clear_period_end apply to an edit")
		}
		in := gtm.CreateCampaignInput{Name: *p.Name, Goal: ptr.From(p.Goal), PeriodStart: start, PeriodEnd: end, Status: gtm.CampaignDraft, StrategyID: strings.TrimSpace(*p.StrategyID)}
		if channels != nil {
			in.PlannedChannelIDs = *channels
		}
		if formats != nil {
			in.PlannedFormats = *formats
		}
		c, err := svc.CreateCampaign(ctx, actor, in)
		if err != nil {
			return gtmFailure(err, "Not found.", true)
		}
		return gtmJSON(gtmCampaign(c))
	}
	in := gtm.UpdateCampaignInput{
		ID: id, ExpectedRevision: p.ExpectedRevision, Name: p.Name, Goal: p.Goal, PeriodStart: start, PeriodEnd: end,
		ClearPeriodStart: p.ClearPeriodStart, ClearPeriodEnd: p.ClearPeriodEnd, PlannedChannelIDs: channels, PlannedFormats: formats,
	}
	if p.StrategyID != nil {
		s := strings.TrimSpace(*p.StrategyID)
		in.StrategyID = &s
	}
	c, err := svc.UpdateCampaign(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, gtmNotFound("campaign", id, ToolListGtmCampaigns), true)
	}
	return gtmJSON(gtmCampaign(c))
}

// =============================================================================
// Calendar items
// =============================================================================

type ListGtmCalendarParams struct {
	ID         string `json:"id,omitempty" jsonschema:"description=One item's id: returns it whole. Omit to list."`
	From       string `json:"from,omitempty" jsonschema:"description=Only items scheduled at or after this (YYYY-MM-DD or RFC 3339)."`
	To         string `json:"to,omitempty" jsonschema:"description=Only items scheduled before this (YYYY-MM-DD or RFC 3339)."`
	Format     string `json:"format,omitempty" jsonschema:"enum=SHORT_POST,enum=LONG_POST,enum=EMAIL,enum=COLD_OUTREACH,enum=VIDEO,enum=MEDIA_PITCH,enum=AD,enum=OTHER,description=Only items of this format."`
	ChannelID  string `json:"channel_id,omitempty" jsonschema:"description=Only items on this channel."`
	CampaignID string `json:"campaign_id,omitempty" jsonschema:"description=Only this campaign's items."`
	Status     string `json:"status,omitempty" jsonschema:"enum=DRAFT,enum=REVIEW,enum=APPROVED,enum=SCHEDULED,enum=SENT,enum=PUBLISHED,enum=PAUSED,description=Only items in this status."`
	PageSize   int    `json:"page_size,omitempty" jsonschema:"description=Items per page: default 20; at most 50."`
	PageToken  string `json:"page_token,omitempty" jsonschema:"description=next_page_token from the previous page."`
}

const listGtmCalendarDescription = "List the org's GTM items (posts, emails, videos, pitches, ads), the content calendar: scheduled ones soonest first, then the rest newest first. from and to make it a calendar range (from inclusive, to exclusive; either one lists only scheduled items). Filter by format, channel, campaign or status, or read one whole with id. Each item shows its id, title, format, channel, status, scheduled_at and revision; save_gtm_item needs the id and revision to edit one."

func listGtmCalendar(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmCalendarParams) ToolResponse {
	if id := strings.TrimSpace(p.ID); id != "" {
		it, err := svc.GetItem(ctx, actor, gtm.GetItemInput{ID: id})
		if err != nil {
			return gtmFailure(err, gtmNotFound("item", id, ToolListGtmCalendar), false)
		}
		return gtmJSON(gtmItem(it, true))
	}
	from, err := gtmTime("from", p.From)
	if err != nil {
		return NewTextErrorResponse(err.Error())
	}
	to, err := gtmTime("to", p.To)
	if err != nil {
		return NewTextErrorResponse(err.Error())
	}
	in := gtm.ListItemsInput{
		Format: gtm.ItemFormat(gtmLong(gtmItemFormat, p.Format)), Status: gtm.ItemStatus(gtmLong(gtmItemStatus, p.Status)),
		ScheduledFrom: from, ScheduledTo: to, PageSize: gtmPage(p.PageSize), PageToken: p.PageToken,
	}
	if c := strings.TrimSpace(p.CampaignID); c != "" {
		in.CampaignID = &c
	}
	if c := strings.TrimSpace(p.ChannelID); c != "" {
		in.ChannelID = &c
	}
	res, err := svc.ListItems(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	out := struct {
		Items         []gtmItemView `json:"items"`
		NextPageToken string        `json:"next_page_token,omitempty"`
	}{Items: []gtmItemView{}, NextPageToken: res.NextPageToken}
	for _, it := range res.Items {
		out.Items = append(out.Items, gtmItem(it, false))
	}
	return gtmJSON(out)
}

type SaveGtmItemParams struct {
	ID               string    `json:"id,omitempty" jsonschema:"description=The item to edit. Omit to create one."`
	ExpectedRevision int32     `json:"expected_revision,omitempty" jsonschema:"description=The revision you read. Required to edit."`
	Status           string    `json:"status,omitempty" jsonschema:"enum=DRAFT,enum=REVIEW,description=DRAFT (the default) or REVIEW: ready for a person to review. Nothing else."`
	Format           string    `json:"format,omitempty" jsonschema:"enum=SHORT_POST,enum=LONG_POST,enum=EMAIL,enum=COLD_OUTREACH,enum=VIDEO,enum=MEDIA_PITCH,enum=AD,enum=OTHER,description=What it is. Required to create. EMAIL and COLD_OUTREACH are emails: they need email_to and email_subject and are sent only with send_gtm_email."`
	Title            *string   `json:"title,omitempty" jsonschema:"description=Required to create."`
	BodyMD           *string   `json:"body_md,omitempty" jsonschema:"description=The content in markdown: the post\\, the email body\\, the script."`
	ChannelID        *string   `json:"channel_id,omitempty" jsonschema:"description=Where it goes out (list_gtm_channels id). On an edit an empty string takes it off its channel."`
	CampaignID       *string   `json:"campaign_id,omitempty" jsonschema:"description=The campaign it is part of. On an edit an empty string unlinks it."`
	IdeaID           *string   `json:"idea_id,omitempty" jsonschema:"description=The idea it comes from. On an edit an empty string unlinks it."`
	ScheduledAt      string    `json:"scheduled_at,omitempty" jsonschema:"description=When it is planned to go out (RFC 3339 or YYYY-MM-DD): a date on the calendar\\, which never publishes it."`
	ClearScheduledAt bool      `json:"clear_scheduled_at,omitempty" jsonschema:"description=Take it off the calendar date."`
	EmailTo          *[]string `json:"email_to,omitempty" jsonschema:"description=An email's recipients (bare addresses). On an edit it replaces the list."`
	EmailSubject     *string   `json:"email_subject,omitempty" jsonschema:"description=An email's subject."`
}

const saveGtmItemDescription = "Draft a GTM item (a post, email, video script, media pitch or ad) or edit one: format, channel_id, title, body_md, scheduled_at, campaign and idea links, and for an email (EMAIL or COLD_OUTREACH) email_to and email_subject. An item you save is DRAFT or REVIEW, and you edit only DRAFT and REVIEW items; set REVIEW when it is ready for a person. scheduled_at puts it on the calendar and never publishes it. This never publishes or sends anything: a person approves an item, posts it and records where it went out; an approved email goes out only through send_gtm_email with their approval card. Check claims first with check_gtm_claims. Edit with id and expected_revision; fields you omit are left alone."

func saveGtmItem(ctx context.Context, svc GTMService, actor gtm.Actor, p SaveGtmItemParams) ToolResponse {
	var status *gtm.ItemStatus
	if s := gtmLong(gtmItemStatus, p.Status); s != "" {
		if s != string(gtm.ItemDraft) && s != string(gtm.ItemReview) {
			return gtmInvalid(fmt.Sprintf("status %s is a person's to set; an agent saves items as DRAFT or REVIEW", gtmShort(gtmItemStatus, s)))
		}
		st := gtm.ItemStatus(s)
		status = &st
	}
	at, err := gtmTime("scheduled_at", p.ScheduledAt)
	if err != nil {
		return gtmInvalid(err.Error())
	}
	var emailTo *[]string
	if p.EmailTo != nil {
		to := gtmIDList(*p.EmailTo)
		emailTo = &to
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		if p.Title == nil || p.Format == "" {
			return gtmInvalid("A new item needs format and title")
		}
		if p.ClearScheduledAt {
			return gtmInvalid("clear_scheduled_at applies to an edit")
		}
		in := gtm.CreateItemInput{
			Format: gtm.ItemFormat(gtmLong(gtmItemFormat, p.Format)), Title: *p.Title, BodyMD: ptr.From(p.BodyMD),
			ChannelID: strings.TrimSpace(ptr.From(p.ChannelID)), CampaignID: strings.TrimSpace(ptr.From(p.CampaignID)),
			IdeaID: strings.TrimSpace(ptr.From(p.IdeaID)), ScheduledAt: at, EmailSubject: ptr.From(p.EmailSubject),
		}
		if status != nil {
			in.Status = *status
		}
		if emailTo != nil {
			in.EmailTo = *emailTo
		}
		it, err := svc.CreateItem(ctx, actor, in)
		if err != nil {
			return gtmFailure(err, "Not found.", true)
		}
		return gtmJSON(gtmItem(it, true))
	}
	in := gtm.UpdateItemInput{
		ID: id, ExpectedRevision: p.ExpectedRevision, Title: p.Title, BodyMD: p.BodyMD, ChannelID: trimPtr(p.ChannelID),
		CampaignID: trimPtr(p.CampaignID), IdeaID: trimPtr(p.IdeaID), ScheduledAt: at, ClearScheduledAt: p.ClearScheduledAt,
		EmailTo: emailTo, EmailSubject: p.EmailSubject, Status: status,
	}
	if p.Format != "" {
		f := gtm.ItemFormat(gtmLong(gtmItemFormat, p.Format))
		in.Format = &f
	}
	it, err := svc.UpdateItem(ctx, actor, in)
	if err != nil {
		return gtmFailure(err, gtmNotFound("item", id, ToolListGtmCalendar), true)
	}
	return gtmJSON(gtmItem(it, true))
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

// =============================================================================
// Channels
// =============================================================================

type ListGtmChannelsParams struct {
	ID     string `json:"id,omitempty" jsonschema:"description=One channel's id. Omit to list."`
	Status string `json:"status,omitempty" jsonschema:"enum=ACTIVE,enum=PAUSED,enum=ARCHIVED,description=Only channels in this status."`
	Mode   string `json:"mode,omitempty" jsonschema:"enum=INTEGRATED,enum=MANUAL,description=Only INTEGRATED channels (sent through a Reliant integration\\, e.g. gmail) or MANUAL ones (a person posts by hand)."`
}

const listGtmChannelsDescription = "List the org's GTM channels, the places items go out (blog, email, hacker-news, reddit, linkedin, x, youtube and any the org added), by key, or read one with id (a list shows at most 50, with the start of each one's instructions; status and mode filter it). Each shows its id, key, name, mode (INTEGRATED through a Reliant integration such as gmail, or MANUAL: a person posts by hand following manual_instructions_md), connection_state for an integrated one (whether the user's integration is connected now), status and revision."

func listGtmChannels(ctx context.Context, svc GTMService, actor gtm.Actor, p ListGtmChannelsParams) ToolResponse {
	if id := strings.TrimSpace(p.ID); id != "" {
		c, err := svc.GetChannel(ctx, actor, gtm.GetChannelInput{ID: id})
		if err != nil {
			return gtmFailure(err, gtmNotFound("channel", id, ToolListGtmChannels), false)
		}
		return gtmJSON(gtmChannel(c, true))
	}
	res, err := svc.ListChannels(ctx, actor, gtm.ListChannelsInput{
		Status: gtm.ChannelStatus(gtmLong(gtmChannelStatus, p.Status)), Mode: gtm.ChannelMode(gtmLong(gtmChannelMode, p.Mode)),
	})
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	out := struct {
		Channels  []gtmChannelView `json:"channels"`
		Truncated bool             `json:"truncated,omitempty"`
	}{Channels: []gtmChannelView{}}
	for i, c := range res.Channels {
		if i == gtmMaxChannels {
			out.Truncated = true
			break
		}
		out.Channels = append(out.Channels, gtmChannel(c, false))
	}
	return gtmJSON(out)
}

type SaveGtmChannelParams struct {
	ID                   string    `json:"id,omitempty" jsonschema:"description=The channel to edit. Omit to add one."`
	ExpectedRevision     int32     `json:"expected_revision,omitempty" jsonschema:"description=The revision you read. Required to edit."`
	Key                  *string   `json:"key,omitempty" jsonschema:"description=A slug unique in the org: lowercase letters\\, digits and hyphens\\, at most 32. Required to add."`
	Name                 *string   `json:"name,omitempty" jsonschema:"description=Required to add."`
	ManualInstructionsMD *string   `json:"manual_instructions_md,omitempty" jsonschema:"description=How a person posts on it by hand\\, in markdown."`
	ChannelTypes         *[]string `json:"channel_types,omitempty" jsonschema:"description=The generic kinds of place it is (e.g. forum or social)\\, which idea-bank entries match on. On an edit it replaces the list."`
	Status               string    `json:"status,omitempty" jsonschema:"enum=ACTIVE,enum=PAUSED,enum=ARCHIVED,description=ACTIVE (the default for a new channel)\\, PAUSED or ARCHIVED."`
}

const saveGtmChannelDescription = "Add a MANUAL GTM channel (a place a person posts by hand, such as a forum or a newsletter), or edit a MANUAL one: key, name, manual_instructions_md, channel_types and status. Every channel you add is MANUAL. You cannot make a channel INTEGRATED, set its integration or connection, or edit an integrated channel: connecting an integration is a person's, through Reliant's connect flow. Edit with id and expected_revision; fields you omit are left alone."

func saveGtmChannel(ctx context.Context, svc GTMService, actor gtm.Actor, p SaveGtmChannelParams) ToolResponse {
	var status *gtm.ChannelStatus
	if s := gtmLong(gtmChannelStatus, p.Status); s != "" {
		st := gtm.ChannelStatus(s)
		status = &st
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		if p.Key == nil || p.Name == nil {
			return gtmInvalid("A new channel needs key and name")
		}
		in := gtm.CreateChannelInput{Key: strings.TrimSpace(*p.Key), Name: *p.Name, Mode: gtm.ChannelManual, ManualInstructionsMD: ptr.From(p.ManualInstructionsMD)}
		if p.ChannelTypes != nil {
			in.ChannelTypes = *p.ChannelTypes
		}
		if status != nil {
			in.Status = *status
		}
		c, err := svc.CreateChannel(ctx, actor, in)
		if err != nil {
			return gtmFailure(err, "Not found.", true)
		}
		return gtmJSON(gtmChannel(c, true))
	}
	c, err := svc.UpdateChannel(ctx, actor, gtm.UpdateChannelInput{
		ID: id, ExpectedRevision: p.ExpectedRevision, Key: trimPtr(p.Key), Name: p.Name,
		ManualInstructionsMD: p.ManualInstructionsMD, ChannelTypes: p.ChannelTypes, Status: status,
	})
	if err != nil {
		return gtmFailure(err, gtmNotFound("channel", id, ToolListGtmChannels), true)
	}
	return gtmJSON(gtmChannel(c, true))
}

// =============================================================================
// Positioning and claims
// =============================================================================

type GetGtmPositioningParams struct{}

const getGtmPositioningDescription = "Read the org's positioning (what the product is, for whom, and why, in markdown), its audiences, and its business type and business models (what list_gtm_strategy_library's fits_my_business matches entries against): the brief every GTM draft starts from. Read-only: a person edits it."

func getGtmPositioning(ctx context.Context, svc GTMService, actor gtm.Actor, _ GetGtmPositioningParams) ToolResponse {
	pos, err := svc.GetPositioning(ctx, actor)
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	out := struct {
		PositioningMD  string   `json:"positioning_md"`
		Audiences      []string `json:"audiences"`
		BusinessType   string   `json:"business_type,omitempty"`
		BusinessModels []string `json:"business_models,omitempty"`
		Revision       int32    `json:"revision"`
		UpdatedAt      string   `json:"updated_at,omitempty"`
		Note           string   `json:"note,omitempty"`
	}{PositioningMD: pos.PositioningMD, Audiences: pos.Audiences, Revision: pos.Revision, UpdatedAt: gtmTimeOut(&pos.UpdatedAt),
		BusinessType: gtmShort(gtmBusinessType, string(pos.BusinessType)), BusinessModels: gtmShorts(gtmBusinessModel, pos.BusinessModels)}
	if out.Audiences == nil {
		out.Audiences = []string{}
	}
	if pos.Revision == 0 {
		out.Note = "The org has no positioning yet; ask the user to write it in the GTM settings."
	}
	return gtmJSON(out)
}

type CheckGtmClaimsParams struct {
	Text   string `json:"text,omitempty" jsonschema:"description=The markdown to check. Give text or item_id."`
	ItemID string `json:"item_id,omitempty" jsonschema:"description=Check this item's title and body instead."`
}

const checkGtmClaimsDescription = "Check a draft's factual claims (prices, capabilities, limits, licenses, timings, comparisons) against the org's claims registry before it goes to review. Each finding is MATCHED_VERIFIED (backed by a verified claim), MATCHED_UNVERIFIED (matches a claim nobody verified yet) or UNMATCHED_RISKY (no claim backs it); every finding but MATCHED_VERIFIED blocks approval. Fix or soften each blocking claim, or flag it for the person reviewing. It stores nothing, and it cannot make a claim verified: only a person verifies a claim."

func checkGtmClaims(ctx context.Context, svc GTMService, actor gtm.Actor, p CheckGtmClaimsParams) ToolResponse {
	text, itemID := p.Text, strings.TrimSpace(p.ItemID)
	switch {
	case strings.TrimSpace(text) != "" && itemID != "":
		return NewTextErrorResponse("Give text or item_id, not both.")
	case itemID != "":
		it, err := svc.GetItem(ctx, actor, gtm.GetItemInput{ID: itemID})
		if err != nil {
			return gtmFailure(err, gtmNotFound("item", itemID, ToolListGtmCalendar), false)
		}
		text = it.Title + "\n\n" + it.BodyMD
	case strings.TrimSpace(text) == "":
		return NewTextErrorResponse("Give text or item_id.")
	}
	res, err := svc.CheckClaims(ctx, actor, gtm.CheckClaimsInput{Text: text})
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	type finding struct {
		Kind      string `json:"kind"`
		Mention   string `json:"mention"`
		Context   string `json:"context,omitempty"`
		ClaimID   string `json:"claim_id,omitempty"`
		ClaimText string `json:"claim_text,omitempty"`
		Reason    string `json:"reason,omitempty"`
		Blocking  bool   `json:"blocking"`
	}
	out := struct {
		Blocking  bool      `json:"blocking"`
		Findings  []finding `json:"findings"`
		Truncated bool      `json:"truncated,omitempty"`
	}{Blocking: res.Blocking, Findings: []finding{}}
	for i, f := range res.Findings {
		if i == gtmDetailCap {
			out.Truncated = true
			break
		}
		out.Findings = append(out.Findings, finding{
			Kind: gtmShort(gtmFindingKind, string(f.Kind)), Mention: f.Mention, Context: f.Context,
			ClaimID: f.ClaimID, ClaimText: f.ClaimText, Reason: f.Reason, Blocking: f.Blocking,
		})
	}
	return gtmJSON(out)
}

// =============================================================================
// Product analytics
// =============================================================================

type ReadProductAnalyticsParams struct {
	Template    string   `json:"template" jsonschema:"required,enum=event_counts,enum=event_trend,enum=property_breakdown,enum=funnel,description=Which aggregate to read; see the tool description."`
	Events      []string `json:"events,omitempty" jsonschema:"description=Event names (letters\\, digits and _ . : - only). event_counts: optional\\, at most 10. event_trend: 1 to 10. property_breakdown: exactly one. funnel: the 2 to 10 steps in order."`
	Property    string   `json:"property,omitempty" jsonschema:"description=property_breakdown only: the property to group the event by."`
	From        string   `json:"from,omitempty" jsonschema:"description=Window start (RFC 3339 or YYYY-MM-DD). Default: 7 days before to."`
	To          string   `json:"to,omitempty" jsonschema:"description=Window end\\, exclusive (RFC 3339 or YYYY-MM-DD). Default: now."`
	Days        int      `json:"days,omitempty" jsonschema:"description=Instead of from: the window is the days before to."`
	Limit       int      `json:"limit,omitempty" jsonschema:"description=Rows: default 50; at most 200."`
	Environment string   `json:"environment,omitempty" jsonschema:"description=Only this deploy environment\\, by name (e.g. prod)\\, matched exactly. Default: all environments."`
	ProjectID   string   `json:"project_id,omitempty" jsonschema:"description=Only this project's events (with environment: that project's environment of that name). Default: every project."`
}

// gtmAllEnvironments labels a result read across every environment.
const gtmAllEnvironments = "all environments"

const readProductAnalyticsDescription = `Read aggregate product analytics for the org's own product: server-built templates over its product events, never raw SQL and never another org. Templates:
- event_counts: the top events by count in the window (optionally only the events named).
- event_trend: daily counts of 1 to 10 events.
- property_breakdown: one event's count grouped by the values of one property.
- funnel: how many accounts reached each of 2 to 10 steps in order, with each step's rate of step 1.
Limits: event_counts, event_trend and property_breakdown read at most 30 days, a funnel at most 90 days; at most 200 rows (truncated says when rows were cut). Groups seen by fewer than 3 signed-in people are hidden and counted in hidden_groups.
Scope: environment (a deploy environment's name, e.g. "prod") and project_id narrow the read. Without them, results cover all environments and are labelled "all environments": that includes preview and local environments and events with no environment, so name environment "prod" for production numbers. An environment name is matched exactly, across all of the org's projects unless project_id is given; a name that matches nothing is refused with the list of available environment names. Results are aggregates only: there are no user ids or other identifiers. Cite what you read as evidence on an idea (save_gtm_idea evidence).`

func readProductAnalytics(ctx context.Context, svc GTMService, actor gtm.Actor, p ReadProductAnalyticsParams) ToolResponse {
	from, err := gtmTime("from", p.From)
	if err != nil {
		return NewTextErrorResponse(err.Error())
	}
	to, err := gtmTime("to", p.To)
	if err != nil {
		return NewTextErrorResponse(err.Error())
	}
	q := gtm.AnalyticsQuery{
		Template: gtm.AnalyticsTemplate(strings.ToLower(strings.TrimSpace(p.Template))), Events: p.Events, Property: strings.TrimSpace(p.Property), Limit: p.Limit,
		Environment: strings.TrimSpace(p.Environment), ProjectID: strings.TrimSpace(p.ProjectID),
	}
	if to != nil {
		q.To = *to
	}
	switch {
	case p.Days < 0:
		return NewTextErrorResponse("days must be positive.")
	case p.Days > 0 && from != nil:
		return NewTextErrorResponse("Give from or days, not both.")
	case p.Days > 0:
		end := q.To
		if end.IsZero() {
			end = time.Now().UTC()
			q.To = end
		}
		q.From = end.AddDate(0, 0, -p.Days)
	case from != nil:
		q.From = *from
	}
	res, err := svc.ReadProductAnalytics(ctx, actor, q)
	if err != nil {
		return gtmFailure(err, "Not found.", false)
	}
	type row struct {
		Event string  `json:"event,omitempty"`
		Day   string  `json:"day,omitempty"`
		Value *string `json:"value,omitempty"`
		Step  int     `json:"step,omitempty"`
		Count int64   `json:"count"`
		Rate  float64 `json:"rate,omitempty"`
	}
	out := struct {
		Template     string `json:"template"`
		From         string `json:"from"`
		To           string `json:"to"`
		Rows         []row  `json:"rows"`
		Environment  string `json:"environment"`
		ProjectID    string `json:"project_id,omitempty"`
		Truncated    bool   `json:"truncated,omitempty"`
		HiddenGroups int    `json:"hidden_groups"`
	}{Template: string(res.Template), From: gtmTimeOut(&res.From), To: gtmTimeOut(&res.To), Rows: []row{}, Environment: res.Environment, ProjectID: res.ProjectID, Truncated: res.Truncated, HiddenGroups: res.HiddenGroups}
	if out.Environment == "" {
		out.Environment = gtmAllEnvironments
	}
	for _, r := range res.Rows {
		v := row{Event: r.Event, Step: r.Step, Count: r.Count, Rate: r.Rate}
		if !r.Day.IsZero() {
			v.Day = r.Day.UTC().Format("2006-01-02")
		}
		if res.Template == gtm.AnalyticsPropertyBreakdown {
			val := r.Value
			v.Value = &val
		}
		out.Rows = append(out.Rows, v)
	}
	return gtmJSON(out)
}

// =============================================================================
// Assets
// =============================================================================

type AttachGtmAssetParams struct {
	AttachmentID string `json:"attachment_id" jsonschema:"required,description=A chat attachment's id: a file the user attached\\, or an image or video generate_image or generate_video made."`
	ItemID       string `json:"item_id,omitempty" jsonschema:"description=The item to attach it to. Give item_id or campaign_id."`
	CampaignID   string `json:"campaign_id,omitempty" jsonschema:"description=The campaign to attach it to."`
}

const attachGtmAssetDescription = "Copy a chat attachment (a file the user attached, or media generate_image or generate_video made) onto a GTM item or campaign as an asset, so the person reviewing it sees it there. The file is checked (size and type) and copied; the attachment itself is unchanged. You cannot delete an asset: a person does."

func attachGtmAsset(ctx context.Context, svc GTMService, actor gtm.Actor, p AttachGtmAssetParams) ToolResponse {
	itemID, campaignID := strings.TrimSpace(p.ItemID), strings.TrimSpace(p.CampaignID)
	if (itemID == "") == (campaignID == "") {
		return gtmInvalid("Give item_id or campaign_id, exactly one")
	}
	a, err := svc.AttachFromAttachment(ctx, actor, gtm.AttachFromAttachmentInput{AttachmentID: strings.TrimSpace(p.AttachmentID), ItemID: itemID, CampaignID: campaignID})
	if err != nil {
		return gtmFailure(err, fmt.Sprintf("No attachment %q, or no such item or campaign; use ids you were given, never guess them.", strings.TrimSpace(p.AttachmentID)), true)
	}
	return gtmJSON(struct {
		ID          string `json:"id"`
		ItemID      string `json:"item_id,omitempty"`
		CampaignID  string `json:"campaign_id,omitempty"`
		Filename    string `json:"filename,omitempty"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
		Status      string `json:"status"`
		Source      string `json:"source,omitempty"`
	}{a.ID, a.ItemID, a.CampaignID, a.Filename, a.ContentType, a.SizeBytes, gtmShort(gtmAssetStatus, string(a.Status)), gtmShort(gtmAssetSource, string(a.Source))})
}

// =============================================================================
// send_gtm_email
// =============================================================================

type SendGtmEmailParams struct {
	ItemID           string `json:"item_id" jsonschema:"required,description=The APPROVED email item to send (list_gtm_calendar)."`
	ExpectedRevision int32  `json:"expected_revision" jsonschema:"required,description=The item's revision as you read it: the one the owner approved."`
}

const sendGtmEmailDescription = "Send an APPROVED GTM email item (format EMAIL or COLD_OUTREACH) through the owner's Gmail, once. Every send shows the owner an approval card with exactly what goes out: recipients, subject, body and the sending account. It is sent only if they approve that card and the item has not changed since. You cannot approve the item yourself: a person sets it to APPROVED in the GTM pages of the Reliant web app. The email is built from the stored item, so edit it with save_gtm_item (which takes it back to review) rather than here. Refused in an unattended run, for an item that is not APPROVED, already SENT, or at another revision, and when no Gmail account is connected; you cannot connect one. A send that may have gone out without being recorded holds the item until a person checks the sent mail."

// gtmUnattendedSend is send_gtm_email's refusal in an unattended run.
const gtmUnattendedSend = "sending a GTM email needs the owner's approval card, and an unattended run cannot show one; nothing was sent"

// sendGtmEmailTool is send_gtm_email: an AutomationApprovalTool whose
// PrepareAutomation raises the owner's card for every attended call and
// refuses an unattended one, and whose execution sends only what the card
// the owner approved for this exact call shows.
type sendGtmEmailTool struct {
	Tool
	repo   runCallerStore
	sender GTMEmailSender
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *sendGtmEmailTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *sendGtmEmailTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type sendGtmEmailTypedTool struct{ outer *sendGtmEmailTool }

func (t *sendGtmEmailTypedTool) Name() string        { return ToolSendGtmEmail }
func (t *sendGtmEmailTypedTool) Description() string { return sendGtmEmailDescription }
func (t *sendGtmEmailTypedTool) RequiresPermission(SendGtmEmailParams) (bool, error) {
	return true, nil
}

func NewSendGtmEmailTool(repo runCallerStore, sender GTMEmailSender) Tool {
	t := &sendGtmEmailTool{repo: repo, sender: sender}
	t.Tool = NewToolWrapper[SendGtmEmailParams, ToolResponse](&sendGtmEmailTypedTool{outer: t})
	return t
}

// decode parses a call's input as the tool would, bindings applied.
func (t *sendGtmEmailTool) decode(call ToolCall) (SendGtmEmailParams, error) {
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		if input, err = ApplyBindings(ToolSendGtmEmail, input, bindings); err != nil {
			return SendGtmEmailParams{}, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p SendGtmEmailParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return SendGtmEmailParams{}, RefuseActivation("tool input is not a valid send_gtm_email call", err)
	}
	return p, nil
}

// PrepareAutomation implements AutomationApprovalTool. A send ALWAYS needs
// the owner's card: an unattended call is refused before anything is read,
// an attended one returns the card with needsApproval true, and anything the
// sender refuses is the refusal, in sanitized words. It never answers "no
// approval needed".
func (t *sendGtmEmailTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	actor, failure := gtmActor(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if unattended {
		// Refused here, whatever the sender would say: nobody can see a card.
		err := errors.New(gtmUnattendedSend)
		return ActivationPreview{}, false, RefuseActivation(gtmUnattendedSend, err)
	}
	if t.sender == nil {
		return ActivationPreview{}, false, RefuseActivation("GTM email is unavailable on this server", errors.New("no GTM email sender"))
	}
	p, err := t.decode(call)
	if err != nil {
		return ActivationPreview{}, false, err
	}
	itemID := strings.TrimSpace(p.ItemID)
	title, hash, err := t.sender.PrepareSend(ctx.Context, actor, itemID, p.ExpectedRevision, false)
	if err != nil {
		// The reason is shown to the agent and the owner: the domain's own
		// words, never raw store or driver text (gtmErrorText).
		return ActivationPreview{}, false, RefuseActivation(gtmErrorText(err, gtmNotFound("item", itemID, ToolListGtmCalendar)), err)
	}
	if hash == "" {
		err := errors.New("the send could not be bound to an approval")
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	return ActivationPreview{Title: title, Hash: hash}, true, nil
}

func (t *sendGtmEmailTypedTool) Execute(ctx *rctx.ToolContext, p SendGtmEmailParams) (ToolResponse, error) {
	o := t.outer
	actor, failure := gtmActor(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	if o.sender == nil {
		return NewTextErrorResponse("GTM email is unavailable on this server. Nothing was sent."), nil
	}
	// The call id keys the approval the owner gave this call's card. It is
	// the runtime's, never the model's.
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	run := gtm.EmailRun{RunID: integrationRunID(ctx.ChatID, ctx.Thread), ToolCallID: callID}
	itemID := strings.TrimSpace(p.ItemID)
	it, err := o.sender.SendApproved(ctx.Context, actor, itemID, p.ExpectedRevision, run)
	if err != nil {
		return NewTextErrorResponse(strings.TrimSuffix(gtmErrorText(err, gtmNotFound("item", itemID, ToolListGtmCalendar)), ".") + "."), nil
	}
	return gtmJSON(struct {
		Sent bool `json:"sent"`
		gtmItemView
	}{true, gtmItem(it, false)}), nil
}

// =============================================================================
// Constructors
// =============================================================================

func NewListGtmIdeasTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmIdeas, listGtmIdeasDescription, listGtmIdeas)
}

func NewSaveGtmIdeaTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolSaveGtmIdea, saveGtmIdeaDescription, saveGtmIdea)
}

func NewListGtmStrategiesTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmStrategies, listGtmStrategiesDescription, listGtmStrategies)
}

func NewSaveGtmStrategyTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolSaveGtmStrategy, saveGtmStrategyDescription, saveGtmStrategy)
}

func NewListGtmStrategyLibraryTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmStrategyLibrary, listGtmStrategyLibraryDescription, listGtmStrategyLibrary)
}

func NewListGtmCampaignsTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmCampaigns, listGtmCampaignsDescription, listGtmCampaigns)
}

func NewSaveGtmCampaignTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolSaveGtmCampaign, saveGtmCampaignDescription, saveGtmCampaign)
}

func NewListGtmCalendarTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmCalendar, listGtmCalendarDescription, listGtmCalendar)
}

func NewSaveGtmItemTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolSaveGtmItem, saveGtmItemDescription, saveGtmItem)
}

func NewListGtmChannelsTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolListGtmChannels, listGtmChannelsDescription, listGtmChannels)
}

func NewSaveGtmChannelTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolSaveGtmChannel, saveGtmChannelDescription, saveGtmChannel)
}

func NewGetGtmPositioningTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolGetGtmPositioning, getGtmPositioningDescription, getGtmPositioning)
}

func NewCheckGtmClaimsTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolCheckGtmClaims, checkGtmClaimsDescription, checkGtmClaims)
}

func NewReadProductAnalyticsTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolReadProductAnalytics, readProductAnalyticsDescription, readProductAnalytics)
}

func NewAttachGtmAssetTool(repo runCallerStore, svc GTMService) Tool {
	return newGTMTool(gtmDeps{repo, svc}, ToolAttachGtmAsset, attachGtmAssetDescription, attachGtmAsset)
}

func (f *ToolsFactory) gtm() GTMService {
	if f.opts == nil || f.opts.GTM == nil {
		return nil
	}
	return f.opts.GTM
}

func (f *ToolsFactory) gtmEmail() GTMEmailSender {
	if f.opts == nil || f.opts.GTMEmail == nil {
		return nil
	}
	return f.opts.GTMEmail
}

func (f *ToolsFactory) ListGtmIdeas() Tool      { return NewListGtmIdeasTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SaveGtmIdea() Tool       { return NewSaveGtmIdeaTool(f.store(), f.gtm()) }
func (f *ToolsFactory) ListGtmStrategies() Tool { return NewListGtmStrategiesTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SaveGtmStrategy() Tool   { return NewSaveGtmStrategyTool(f.store(), f.gtm()) }
func (f *ToolsFactory) ListGtmStrategyLibrary() Tool {
	return NewListGtmStrategyLibraryTool(f.store(), f.gtm())
}
func (f *ToolsFactory) ListGtmCampaigns() Tool  { return NewListGtmCampaignsTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SaveGtmCampaign() Tool   { return NewSaveGtmCampaignTool(f.store(), f.gtm()) }
func (f *ToolsFactory) ListGtmCalendar() Tool   { return NewListGtmCalendarTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SaveGtmItem() Tool       { return NewSaveGtmItemTool(f.store(), f.gtm()) }
func (f *ToolsFactory) ListGtmChannels() Tool   { return NewListGtmChannelsTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SaveGtmChannel() Tool    { return NewSaveGtmChannelTool(f.store(), f.gtm()) }
func (f *ToolsFactory) GetGtmPositioning() Tool { return NewGetGtmPositioningTool(f.store(), f.gtm()) }
func (f *ToolsFactory) CheckGtmClaims() Tool    { return NewCheckGtmClaimsTool(f.store(), f.gtm()) }
func (f *ToolsFactory) ReadProductAnalytics() Tool {
	return NewReadProductAnalyticsTool(f.store(), f.gtm())
}
func (f *ToolsFactory) AttachGtmAsset() Tool { return NewAttachGtmAssetTool(f.store(), f.gtm()) }
func (f *ToolsFactory) SendGtmEmail() Tool   { return NewSendGtmEmailTool(f.store(), f.gtmEmail()) }
