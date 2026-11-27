// Copyright (c) 2026 Reliant Labs

package gtm

import "time"

// Enum values are the GtmService proto enum value NAMES, which is also what
// the gtm_* tables store ('GTM_IDEA_STATUS_PROPOSED'). The empty string is
// "unset": a filter that matches anything, or a field left alone.

// IdeaKind is what an idea is about.
type IdeaKind string

// IdeaKind values.
const (
	IdeaKindProduct IdeaKind = "GTM_IDEA_KIND_PRODUCT"
	IdeaKindGTM     IdeaKind = "GTM_IDEA_KIND_GTM"
	IdeaKindContent IdeaKind = "GTM_IDEA_KIND_CONTENT"
)

// IdeaStatus is where an idea is: PROPOSED, then ACCEPTED then DONE, or
// REJECTED.
type IdeaStatus string

// IdeaStatus values.
const (
	IdeaProposed IdeaStatus = "GTM_IDEA_STATUS_PROPOSED"
	IdeaAccepted IdeaStatus = "GTM_IDEA_STATUS_ACCEPTED"
	IdeaDone     IdeaStatus = "GTM_IDEA_STATUS_DONE"
	IdeaRejected IdeaStatus = "GTM_IDEA_STATUS_REJECTED"
)

// IdeaSource is who proposed an idea.
type IdeaSource string

// IdeaSource values.
const (
	IdeaSourceAgent IdeaSource = "GTM_IDEA_SOURCE_AGENT"
	IdeaSourceHuman IdeaSource = "GTM_IDEA_SOURCE_HUMAN"
)

// CampaignStatus is a campaign's lifecycle state.
type CampaignStatus string

// CampaignStatus values.
const (
	CampaignDraft    CampaignStatus = "GTM_CAMPAIGN_STATUS_DRAFT"
	CampaignActive   CampaignStatus = "GTM_CAMPAIGN_STATUS_ACTIVE"
	CampaignDone     CampaignStatus = "GTM_CAMPAIGN_STATUS_DONE"
	CampaignArchived CampaignStatus = "GTM_CAMPAIGN_STATUS_ARCHIVED"
)

// ItemStatus is where an item is. SENT is written only by the email path.
type ItemStatus string

// ItemStatus values.
const (
	ItemDraft     ItemStatus = "GTM_ITEM_STATUS_DRAFT"
	ItemReview    ItemStatus = "GTM_ITEM_STATUS_REVIEW"
	ItemApproved  ItemStatus = "GTM_ITEM_STATUS_APPROVED"
	ItemScheduled ItemStatus = "GTM_ITEM_STATUS_SCHEDULED"
	ItemSent      ItemStatus = "GTM_ITEM_STATUS_SENT"
	ItemPublished ItemStatus = "GTM_ITEM_STATUS_PUBLISHED"
	ItemPaused    ItemStatus = "GTM_ITEM_STATUS_PAUSED"
)

// ClaimCategory is what a claim states.
type ClaimCategory string

// ClaimCategory values.
const (
	ClaimPricing    ClaimCategory = "GTM_CLAIM_CATEGORY_PRICING"
	ClaimCapability ClaimCategory = "GTM_CLAIM_CATEGORY_CAPABILITY"
	ClaimLimit      ClaimCategory = "GTM_CLAIM_CATEGORY_LIMIT"
	ClaimLicense    ClaimCategory = "GTM_CLAIM_CATEGORY_LICENSE"
	ClaimTiming     ClaimCategory = "GTM_CLAIM_CATEGORY_TIMING"
	ClaimComparison ClaimCategory = "GTM_CLAIM_CATEGORY_COMPARISON"
)

// ClaimStatus is a registry claim's state.
type ClaimStatus string

// ClaimStatus values.
const (
	ClaimVerified   ClaimStatus = "GTM_CLAIM_STATUS_VERIFIED"
	ClaimUnverified ClaimStatus = "GTM_CLAIM_STATUS_UNVERIFIED"
	ClaimRetired    ClaimStatus = "GTM_CLAIM_STATUS_RETIRED"
)

// ClaimSourceKind is what backs a verified claim.
type ClaimSourceKind string

// ClaimSourceKind values. ClaimSourceNone is stored: "no source yet".
const (
	ClaimSourceNone              ClaimSourceKind = "GTM_CLAIM_SOURCE_KIND_UNSPECIFIED"
	ClaimSourceDocs              ClaimSourceKind = "GTM_CLAIM_SOURCE_KIND_DOCS"
	ClaimSourcePricingCatalog    ClaimSourceKind = "GTM_CLAIM_SOURCE_KIND_PRICING_CATALOG"
	ClaimSourceCode              ClaimSourceKind = "GTM_CLAIM_SOURCE_KIND_CODE"
	ClaimSourcePrimaryThirdParty ClaimSourceKind = "GTM_CLAIM_SOURCE_KIND_PRIMARY_THIRD_PARTY"
)

// ClaimFindingKind is what the registry says about one mention in a text.
type ClaimFindingKind string

// ClaimFindingKind values.
const (
	FindingMatchedVerified   ClaimFindingKind = "GTM_CLAIM_FINDING_KIND_MATCHED_VERIFIED"
	FindingMatchedUnverified ClaimFindingKind = "GTM_CLAIM_FINDING_KIND_MATCHED_UNVERIFIED"
	FindingUnmatchedRisky    ClaimFindingKind = "GTM_CLAIM_FINDING_KIND_UNMATCHED_RISKY"
)

// Access is whether an actor may use GTM.
type Access struct {
	Enabled bool
}

// Evidence is one thing behind an idea: an analytics readout, a link, a note.
type Evidence struct {
	Kind       string     `json:"kind,omitempty"`
	Title      string     `json:"title,omitempty"`
	URL        string     `json:"url,omitempty"`
	SummaryMD  string     `json:"summaryMd,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}

// Idea is something to build, a go-to-market move or content to write, with
// the evidence behind it.
type Idea struct {
	ID           string
	OrgID        string
	Kind         IdeaKind
	Title        string
	BodyMD       string
	Evidence     []Evidence
	Status       IdeaStatus
	Source       IdeaSource
	SourceChatID string
	CreatedBy    string
	// CampaignID is "" for none.
	CampaignID string
	Revision   int32
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// LibraryKey is the idea bank entry it came from; "" for none.
	LibraryKey string
	// TailorAnswers are that entry's tailor questions, answered for this
	// business.
	TailorAnswers []TailorAnswer
	// SuggestedWorkflowMD is a tool-agnostic description of an automation
	// that carries the idea out (the entry's, tailored); "" for none.
	SuggestedWorkflowMD string
}

// TailorAnswer is one of an idea bank entry's tailor questions and its
// answer for this business.
type TailorAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Campaign groups ideas and items toward one goal over a period.
type Campaign struct {
	ID          string
	OrgID       string
	Name        string
	Goal        string
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	Status      CampaignStatus
	// StrategyID is the strategy it belongs to (every campaign has one).
	StrategyID string
	// PlannedChannelIDs and PlannedFormats are what it plans to use.
	PlannedChannelIDs []string
	PlannedFormats    []ItemFormat
	Revision          int32
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// CampaignDetail is a campaign with the ideas and items linked to it, each
// list capped at MaxCampaignIdeas / MaxCampaignItems.
type CampaignDetail struct {
	Campaign Campaign
	// Ideas are newest first.
	Ideas []Idea
	// Items are scheduled ones soonest first, then the rest newest first.
	Items []Item
	// Truncated is true when either list held more than its cap; ListIdeas
	// and ListItems with the campaign page through all of them.
	Truncated bool
	// Coverage is planned against actual over ALL its items: the planned
	// pairs first (PlannedChannelIDs order, then PlannedFormats order), then
	// the unplanned pairs its items use.
	Coverage []CoverageRow
}

// Item is a post, an email, a video, a pitch: with ScheduledAt it is on the
// calendar.
type Item struct {
	ID    string
	OrgID string
	// CampaignID and IdeaID are "" for none.
	CampaignID string
	IdeaID     string
	Format     ItemFormat
	// ChannelID is the channel it goes out on; "" for none yet.
	ChannelID   string
	Title       string
	BodyMD      string
	Status      ItemStatus
	ScheduledAt *time.Time
	// EmailTo and EmailSubject are an email-type item's.
	EmailTo      []string
	EmailSubject string
	// The send record, set with SENT.
	GmailMessageID string
	SentAt         *time.Time
	SentBy         string
	ApprovalHash   string
	// PublishedURL is where it went out, recorded by a person; "" for none.
	PublishedURL string
	Revision     int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Positioning is the org's positioning and audiences. Revision 0 is "none yet".
type Positioning struct {
	OrgID         string
	PositioningMD string
	Audiences     []string
	UpdatedBy     string
	Revision      int32
	UpdatedAt     time.Time
	// BusinessType is what kind of business the org is; "" for not said.
	BusinessType BusinessType
	// BusinessModels is how it makes money (possibly several); empty for
	// not said. Both are what the library's FitsMyBusiness reads.
	BusinessModels []BusinessModel
}

// Claim is one registry fact claimcheck matches a text's claims against.
type Claim struct {
	ID       string
	OrgID    string
	Text     string
	TextNorm string
	Category ClaimCategory
	Status   ClaimStatus
	// The evidence a VERIFIED claim carries.
	SourceURL  string
	SourceKind ClaimSourceKind
	VerifiedAt *time.Time
	VerifiedBy string
	// ExpiresOrRecheckAt, once passed, makes a verified claim stale.
	ExpiresOrRecheckAt *time.Time
	Notes              string
	Revision           int32
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ClaimFinding is one mention claimcheck found in a text, and its outcome.
type ClaimFinding struct {
	Kind ClaimFindingKind
	// MentionKind is the rule that found it (claimcheck.Kind*).
	MentionKind string
	// Mention is the words in the text; Context the sentence around them.
	Mention string
	Context string
	// ClaimID and ClaimText are the registry claim it matched, if any.
	ClaimID   string
	ClaimText string
	// Reason says why a matched claim does not vouch for it.
	Reason string
	// Blocking is true for every kind but MATCHED_VERIFIED.
	Blocking bool
}

// Paging. A page size of 0 is the default; above the maximum is refused.
// A page token is the previous result's NextPageToken; "" is the first page,
// and an empty NextPageToken means the last.
const (
	DefaultIdeaPageSize     = 50
	MaxIdeaPageSize         = 200
	DefaultCampaignPageSize = 50
	MaxCampaignPageSize     = 200
	DefaultItemPageSize     = 100
	MaxItemPageSize         = 500
	DefaultClaimPageSize    = 200
	MaxClaimPageSize        = 500

	// MaxCampaignIdeas and MaxCampaignItems cap GetCampaign's lists.
	MaxCampaignIdeas = 200
	MaxCampaignItems = 200
	// MaxCheckedClaims caps the registry CheckClaims matches against.
	MaxCheckedClaims = 5000
)

// Field limits, matching the GtmService proto's.
const (
	MaxTitleLen        = 300
	MaxCampaignNameLen = 200
	MaxClaimTextLen    = 500
	MaxCheckTextLen    = 200000
	MaxBodyLen         = 200000
	MaxShortTextLen    = 10000
	MaxEvidence        = 50
	MaxEmailRecipients = 50
	MaxAudiences       = 50
	MaxURLLen          = 2048
	// MaxTailorAnswers caps an idea's tailor answers; MaxTailorAnswerLen
	// each question and each answer.
	MaxTailorAnswers   = 20
	MaxTailorAnswerLen = 2000
	// MaxSuggestedWorkflowLen caps an idea's suggested_workflow_md.
	MaxSuggestedWorkflowLen = 20000
)

// ListIdeasInput is the input to Service.ListIdeas.
type ListIdeasInput struct {
	// Kind and Status filter when set.
	Kind   IdeaKind
	Status IdeaStatus
	// CampaignID filters when non-nil; a pointer to "" lists unlinked ideas.
	CampaignID *string
	PageSize   int
	PageToken  string
}

// ListIdeasResult is the result of Service.ListIdeas.
type ListIdeasResult struct {
	Ideas         []Idea
	NextPageToken string
}

// GetIdeaInput is the input to Service.GetIdea.
type GetIdeaInput struct {
	ID string
}

// CreateIdeaInput is the input to Service.CreateIdea. The source and source
// chat come from the Actor, never from here.
type CreateIdeaInput struct {
	Kind     IdeaKind
	Title    string
	BodyMD   string
	Evidence []Evidence
	// CampaignID links a campaign of the org; "" for none.
	CampaignID string
	// LibraryKey names the idea bank entry it comes from; "" for none. A
	// key the bank does not hold is InvalidArgument.
	LibraryKey string
	// TailorAnswers: at most MaxTailorAnswers, each question 1 to and each
	// answer at most MaxTailorAnswerLen characters.
	TailorAnswers []TailorAnswer
	// SuggestedWorkflowMD: at most MaxSuggestedWorkflowLen characters.
	SuggestedWorkflowMD string
}

// UpdateIdeaInput is the input to Service.UpdateIdea. A nil field is left
// alone.
type UpdateIdeaInput struct {
	ID               string
	ExpectedRevision int32
	Kind             *IdeaKind
	Title            *string
	BodyMD           *string
	// Evidence, when non-nil, replaces the whole list (empty clears it).
	Evidence *[]Evidence
	// CampaignID, when non-nil, links that campaign; "" unlinks.
	CampaignID *string
	// LibraryKey, when non-nil, names the bank entry; "" clears it.
	LibraryKey *string
	// TailorAnswers, when non-nil, replaces the whole list (empty clears it).
	TailorAnswers *[]TailorAnswer
	// SuggestedWorkflowMD, when non-nil, replaces it.
	SuggestedWorkflowMD *string
}

// SetIdeaStatusInput is the input to Service.SetIdeaStatus.
type SetIdeaStatusInput struct {
	ID               string
	ExpectedRevision int32
	Status           IdeaStatus
}

// ListCampaignsInput is the input to Service.ListCampaigns.
type ListCampaignsInput struct {
	Status CampaignStatus
	// StrategyID filters when non-nil (it may not point at "").
	StrategyID *string
	PageSize   int
	PageToken  string
}

// ListCampaignsResult is the result of Service.ListCampaigns.
type ListCampaignsResult struct {
	Campaigns     []Campaign
	NextPageToken string
}

// GetCampaignInput is the input to Service.GetCampaign.
type GetCampaignInput struct {
	ID string
}

// CreateCampaignInput is the input to Service.CreateCampaign.
type CreateCampaignInput struct {
	Name        string
	Goal        string
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	// Status "" is DRAFT.
	Status CampaignStatus
	// StrategyID is required: a strategy of the org.
	StrategyID string
	// PlannedChannelIDs are channels of the org; PlannedFormats formats.
	PlannedChannelIDs []string
	PlannedFormats    []ItemFormat
}

// UpdateCampaignInput is the input to Service.UpdateCampaign. A nil field is
// left alone; a Clear flag wins over a value.
type UpdateCampaignInput struct {
	ID               string
	ExpectedRevision int32
	Name             *string
	Goal             *string
	PeriodStart      *time.Time
	PeriodEnd        *time.Time
	ClearPeriodStart bool
	ClearPeriodEnd   bool
	Status           *CampaignStatus
	// StrategyID, when set, moves it to that strategy of the org.
	StrategyID *string
	// PlannedChannelIDs and PlannedFormats, when non-nil, replace the lists
	// (empty clears them).
	PlannedChannelIDs *[]string
	PlannedFormats    *[]ItemFormat
}

// ListItemsInput is the input to Service.ListItems.
type ListItemsInput struct {
	// CampaignID filters when non-nil; a pointer to "" lists unlinked items.
	CampaignID *string
	Format     ItemFormat
	// ChannelID filters when non-nil; a pointer to "" lists items with no
	// channel.
	ChannelID *string
	Status    ItemStatus
	// The scheduled range, half-open: ScheduledFrom <= scheduled_at <
	// ScheduledTo. Either bound set lists only scheduled items.
	ScheduledFrom *time.Time
	ScheduledTo   *time.Time
	PageSize      int
	PageToken     string
}

// ListItemsResult is the result of Service.ListItems.
type ListItemsResult struct {
	Items         []Item
	NextPageToken string
}

// GetItemInput is the input to Service.GetItem.
type GetItemInput struct {
	ID string
}

// CreateItemInput is the input to Service.CreateItem.
type CreateItemInput struct {
	// Status is DRAFT or REVIEW; "" is DRAFT.
	Status ItemStatus
	Format ItemFormat
	Title  string
	BodyMD string
	// ChannelID is a channel of the org; "" for none yet.
	ChannelID string
	// CampaignID and IdeaID link rows of the org; "" for none.
	CampaignID  string
	IdeaID      string
	ScheduledAt *time.Time
	// EmailTo and EmailSubject are for an email-type item only.
	EmailTo      []string
	EmailSubject string
}

// UpdateItemInput is the input to Service.UpdateItem. A nil field is left
// alone; ClearScheduledAt wins over ScheduledAt.
type UpdateItemInput struct {
	ID               string
	ExpectedRevision int32
	Format           *ItemFormat
	Title            *string
	BodyMD           *string
	// ChannelID, when non-nil, moves it to that channel; "" takes it off.
	ChannelID *string
	// CampaignID and IdeaID, when non-nil, link that row; "" unlinks.
	CampaignID       *string
	IdeaID           *string
	ScheduledAt      *time.Time
	ClearScheduledAt bool
	// EmailTo, when non-nil, replaces the recipients.
	EmailTo      *[]string
	EmailSubject *string
	// Status is DRAFT, REVIEW, APPROVED (a person only), SCHEDULED,
	// PUBLISHED or PAUSED; never SENT.
	Status *ItemStatus
	// PublishedURL, when non-nil, records where it went out (http or
	// https; "" clears it). A person's act; not a content edit.
	PublishedURL *string
}

// ResolveInDoubtSendInput is the input to Service.ResolveInDoubtSend.
type ResolveInDoubtSendInput struct {
	ItemID string
	// ApprovalHash is the hold's: the card the in-doubt send ran under.
	ApprovalHash string
	// GmailMessageID, when set, is the message the person found in the sent
	// mail, and the item is recorded SENT with it. It is the person's manual
	// marker, stored as given: nothing checks it against Gmail. nil releases
	// the hold ("it did not go out"), refused while the send may still be
	// running, and voids every earlier approval card.
	GmailMessageID *string
}

// SendItemEmailInput is the input to Service.SendItemEmail.
type SendItemEmailInput struct {
	ItemID string
	// ExpectedRevision is the revision the person approved.
	ExpectedRevision int32
	// ApprovalHash is the approval card's hash, which must be ApprovalHash
	// of the item as stored; it is recorded with the send.
	ApprovalHash string
	// Run is the credential context the email is sent with.
	Run EmailRun
}

// EmailRun is the run an email is sent from: the Gmail credential is the
// run owner's, resolved from the run record (connauth), never supplied.
type EmailRun struct {
	RunID      string
	NodeID     string
	ToolCallID string
	// ConnectionID names a Gmail connection of the owner's; "" is their
	// default.
	ConnectionID string
}

// EmailMessage is one email to send.
type EmailMessage struct {
	To      []string
	Subject string
	// BodyMD is the body, markdown.
	BodyMD string
}

// UpdatePositioningInput is the input to Service.UpdatePositioning.
type UpdatePositioningInput struct {
	// ExpectedRevision is the revision read; 0 when the org has none yet.
	ExpectedRevision int32
	PositioningMD    string
	Audiences        []string
	// BusinessType: "" for not said.
	BusinessType BusinessType
	// BusinessModels: each at most once; empty for not said.
	BusinessModels []BusinessModel
}

// ListClaimsInput is the input to Service.ListClaims.
type ListClaimsInput struct {
	Status   ClaimStatus
	Category ClaimCategory
	// Search matches claims whose text contains it, case-insensitively.
	Search    string
	PageSize  int
	PageToken string
}

// ListClaimsResult is the result of Service.ListClaims.
type ListClaimsResult struct {
	Claims        []Claim
	NextPageToken string
}

// UpsertClaimInput is the input to Service.UpsertClaim. With no ID it creates
// a claim (Text and Category required; Status nil is UNVERIFIED). With an ID
// it changes that claim at ExpectedRevision: Text and Category must be nil or
// equal to the stored ones. A nil field is left alone; the Clear flag wins.
type UpsertClaimInput struct {
	ID                      string
	ExpectedRevision        int32
	Text                    *string
	Category                *ClaimCategory
	Status                  *ClaimStatus
	SourceURL               *string
	SourceKind              *ClaimSourceKind
	ExpiresOrRecheckAt      *time.Time
	ClearExpiresOrRecheckAt bool
	Notes                   *string
}

// UpsertClaimResult is the result of Service.UpsertClaim.
type UpsertClaimResult struct {
	Claim Claim
	// Created is true when this call created the claim.
	Created bool
}

// CheckClaimsInput is the input to Service.CheckClaims.
type CheckClaimsInput struct {
	// Text is the markdown to check (an item's title and body).
	Text string
}

// CheckClaimsResult is the result of Service.CheckClaims.
type CheckClaimsResult struct {
	Findings []ClaimFinding
	// Blocking is true when any finding blocks approval.
	Blocking bool
}

// Store filters. OrgID is required; every other zero field matches anything.
// Limit and Offset page the result.

// IdeaFilter selects ideas for Store.ListIdeas.
type IdeaFilter struct {
	OrgID      string
	Kind       IdeaKind
	Status     IdeaStatus
	CampaignID *string
	Limit      int
	Offset     int
}

// CampaignFilter selects campaigns for Store.ListCampaigns.
type CampaignFilter struct {
	OrgID      string
	Status     CampaignStatus
	StrategyID string
	Limit      int
	Offset     int
}

// ItemFilter selects items for Store.ListItems.
type ItemFilter struct {
	OrgID         string
	CampaignID    *string
	Format        ItemFormat
	ChannelID     *string
	Status        ItemStatus
	ScheduledFrom *time.Time
	ScheduledTo   *time.Time
	Limit         int
	Offset        int
}

// ClaimFilter selects claims for Store.ListClaims.
type ClaimFilter struct {
	OrgID    string
	Status   ClaimStatus
	Category ClaimCategory
	Search   string
	Limit    int
	Offset   int
}
