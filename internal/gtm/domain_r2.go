// Copyright (c) 2026 Reliant Labs

package gtm

import "time"

// Strategies, channels, formats, assets and the strategy library (scope
// r2/r3). Enum values are the proto enum value names, as in domain.go.

// ItemFormat is what an item is, apart from where it goes (its channel).
type ItemFormat string

// ItemFormat values. EMAIL and COLD_OUTREACH are the email-type formats.
const (
	FormatShortPost    ItemFormat = "GTM_ITEM_FORMAT_SHORT_POST"
	FormatLongPost     ItemFormat = "GTM_ITEM_FORMAT_LONG_POST"
	FormatEmail        ItemFormat = "GTM_ITEM_FORMAT_EMAIL"
	FormatColdOutreach ItemFormat = "GTM_ITEM_FORMAT_COLD_OUTREACH"
	FormatVideo        ItemFormat = "GTM_ITEM_FORMAT_VIDEO"
	FormatMediaPitch   ItemFormat = "GTM_ITEM_FORMAT_MEDIA_PITCH"
	FormatAd           ItemFormat = "GTM_ITEM_FORMAT_AD"
	FormatOther        ItemFormat = "GTM_ITEM_FORMAT_OTHER"
)

// EmailType reports whether f is sent through the email send path: never
// SCHEDULED or PUBLISHED, SENT only from APPROVED.
func (f ItemFormat) EmailType() bool { return f == FormatEmail || f == FormatColdOutreach }

// StrategyStatus is a strategy's lifecycle.
type StrategyStatus string

// StrategyStatus values. An agent creates and edits DRAFT only.
const (
	StrategyDraft    StrategyStatus = "GTM_STRATEGY_STATUS_DRAFT"
	StrategyActive   StrategyStatus = "GTM_STRATEGY_STATUS_ACTIVE"
	StrategyDone     StrategyStatus = "GTM_STRATEGY_STATUS_DONE"
	StrategyArchived StrategyStatus = "GTM_STRATEGY_STATUS_ARCHIVED"
)

// StrategyCategory is a strategy's or library entry's category: the 13
// categories of the strategy catalog.
type StrategyCategory string

// StrategyCategory values.
const (
	CategoryPricingOffersIncentives   StrategyCategory = "GTM_STRATEGY_CATEGORY_PRICING_OFFERS_INCENTIVES"
	CategoryReferralViralGrowth       StrategyCategory = "GTM_STRATEGY_CATEGORY_REFERRAL_VIRAL_GROWTH"
	CategoryPaidAcquisition           StrategyCategory = "GTM_STRATEGY_CATEGORY_PAID_ACQUISITION"
	CategoryOrganicContentSEO         StrategyCategory = "GTM_STRATEGY_CATEGORY_ORGANIC_CONTENT_SEO"
	CategoryOutboundSalesProspecting  StrategyCategory = "GTM_STRATEGY_CATEGORY_OUTBOUND_SALES_PROSPECTING"
	CategoryPartnershipsDistribution  StrategyCategory = "GTM_STRATEGY_CATEGORY_PARTNERSHIPS_DISTRIBUTION"
	CategoryLaunchesPR                StrategyCategory = "GTM_STRATEGY_CATEGORY_LAUNCHES_PR"
	CategoryCommunityEvents           StrategyCategory = "GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS"
	CategoryProductLedGrowth          StrategyCategory = "GTM_STRATEGY_CATEGORY_PRODUCT_LED_GROWTH"
	CategoryConversionOptimization    StrategyCategory = "GTM_STRATEGY_CATEGORY_CONVERSION_OPTIMIZATION"
	CategoryRetentionExpansion        StrategyCategory = "GTM_STRATEGY_CATEGORY_RETENTION_EXPANSION"
	CategoryTrustReputation           StrategyCategory = "GTM_STRATEGY_CATEGORY_TRUST_REPUTATION"
	CategoryMarketResearchPositioning StrategyCategory = "GTM_STRATEGY_CATEGORY_MARKET_RESEARCH_POSITIONING"
)

// BusinessType is what kind of business an org is (its positioning), and
// what kinds an idea bank entry fits. The values are the proto enum names;
// the bank's lowercase slugs map 1:1 (bankBusinessType).
type BusinessType string

// BusinessType values.
const (
	BusinessB2BSaaS        BusinessType = "GTM_BUSINESS_TYPE_B2B_SAAS"
	BusinessB2CApp         BusinessType = "GTM_BUSINESS_TYPE_B2C_APP"
	BusinessDeveloperTools BusinessType = "GTM_BUSINESS_TYPE_DEVELOPER_TOOLS"
	BusinessEcommerce      BusinessType = "GTM_BUSINESS_TYPE_ECOMMERCE"
	BusinessMarketplace    BusinessType = "GTM_BUSINESS_TYPE_MARKETPLACE"
	BusinessAgencyServices BusinessType = "GTM_BUSINESS_TYPE_AGENCY_SERVICES"
	BusinessCreatorMedia   BusinessType = "GTM_BUSINESS_TYPE_CREATOR_MEDIA"
	BusinessLocalBusiness  BusinessType = "GTM_BUSINESS_TYPE_LOCAL_BUSINESS"
	BusinessOpenSource     BusinessType = "GTM_BUSINESS_TYPE_OPEN_SOURCE"
	BusinessHardware       BusinessType = "GTM_BUSINESS_TYPE_HARDWARE"
)

// BusinessModel is how an org makes money (its positioning, possibly
// several), and which models an idea bank entry fits. Proto enum names.
type BusinessModel string

// BusinessModel values.
const (
	ModelSubscription BusinessModel = "GTM_BUSINESS_MODEL_SUBSCRIPTION"
	ModelUsageBased   BusinessModel = "GTM_BUSINESS_MODEL_USAGE_BASED"
	ModelOneTime      BusinessModel = "GTM_BUSINESS_MODEL_ONE_TIME"
	ModelFreemium     BusinessModel = "GTM_BUSINESS_MODEL_FREEMIUM"
	ModelFreeOSS      BusinessModel = "GTM_BUSINESS_MODEL_FREE_OSS"
	ModelMarketplace  BusinessModel = "GTM_BUSINESS_MODEL_MARKETPLACE"
	ModelServices     BusinessModel = "GTM_BUSINESS_MODEL_SERVICES"
	ModelAdSupported  BusinessModel = "GTM_BUSINESS_MODEL_AD_SUPPORTED"
)

// ChannelMode is how items go out on a channel.
type ChannelMode string

// ChannelMode values.
const (
	ChannelIntegrated ChannelMode = "GTM_CHANNEL_MODE_INTEGRATED"
	ChannelManual     ChannelMode = "GTM_CHANNEL_MODE_MANUAL"
)

// ChannelStatus is a channel's lifecycle.
type ChannelStatus string

// ChannelStatus values.
const (
	ChannelActive   ChannelStatus = "GTM_CHANNEL_STATUS_ACTIVE"
	ChannelPaused   ChannelStatus = "GTM_CHANNEL_STATUS_PAUSED"
	ChannelArchived ChannelStatus = "GTM_CHANNEL_STATUS_ARCHIVED"
)

// ChannelSource is who added a channel.
type ChannelSource string

// ChannelSource values.
const (
	ChannelSourceSeed   ChannelSource = "GTM_CHANNEL_SOURCE_SEED"
	ChannelSourceAgent  ChannelSource = "GTM_CHANNEL_SOURCE_AGENT"
	ChannelSourcePerson ChannelSource = "GTM_CHANNEL_SOURCE_PERSON"
)

// ConnectionState is whether the caller can reach an INTEGRATED channel's
// integration now. Display only: never stored, never authorization.
type ConnectionState string

// ConnectionState values. "" (unspecified) is a MANUAL channel's.
const (
	ConnectionConnected    ConnectionState = "GTM_CONNECTION_STATE_CONNECTED"
	ConnectionNotConnected ConnectionState = "GTM_CONNECTION_STATE_NOT_CONNECTED"
	ConnectionUnknown      ConnectionState = "GTM_CONNECTION_STATE_UNKNOWN"
)

// AssetStatus is whether an asset's bytes are in place.
type AssetStatus string

// AssetStatus values.
const (
	AssetPending AssetStatus = "GTM_ASSET_STATUS_PENDING"
	AssetReady   AssetStatus = "GTM_ASSET_STATUS_READY"
)

// AssetSource is where an asset came from.
type AssetSource string

// AssetSource values.
const (
	AssetSourceChat     AssetSource = "GTM_ASSET_SOURCE_CHAT"
	AssetSourceWorkflow AssetSource = "GTM_ASSET_SOURCE_WORKFLOW"
	AssetSourcePerson   AssetSource = "GTM_ASSET_SOURCE_PERSON"
)

// Strategy is a goal over a period, and the campaigns that pursue it.
type Strategy struct {
	ID           string
	OrgID        string
	Name         string
	GoalMD       string
	TargetMetric string
	// PeriodStart and PeriodEnd are calendar dates, YYYY-MM-DD; "" is open.
	PeriodStart string
	PeriodEnd   string
	Status      StrategyStatus
	// Category is "" for none.
	Category StrategyCategory
	// LibraryKey is the library entry it was made from; "" for none.
	LibraryKey string
	Revision   int32
	CreatedBy  string
	UpdatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StrategyDetail is a strategy with its campaigns, newest first, at most
// MaxStrategyCampaigns.
type StrategyDetail struct {
	Strategy  Strategy
	Campaigns []Campaign
	// Truncated is true when the campaigns were cut at the cap.
	Truncated bool
}

// Channel is one place items go out.
type Channel struct {
	ID    string
	OrgID string
	// Key is a slug unique in the org, fixed at creation.
	Key  string
	Name string
	Mode ChannelMode
	// IntegrationKind and ConnectionID are an INTEGRATED channel's; an empty
	// ConnectionID is the acting user's default connection.
	IntegrationKind      string
	ConnectionID         string
	ManualInstructionsMD string
	// ChannelTypes are the generic kinds of place it is ("forum", "social"):
	// what a library entry's channel_types match.
	ChannelTypes []string
	Status       ChannelStatus
	// ConnectionState is computed per read for the caller.
	ConnectionState ConnectionState
	Source          ChannelSource
	// CreatedBy is "" for a seeded channel.
	CreatedBy string
	Revision  int32
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CoverageRow is one (channel, format) pair of a campaign, planned or used,
// with its items counted by status. A "" channel is items with no channel;
// a "" format is a planned channel when the campaign planned no format (and
// the reverse).
type CoverageRow struct {
	ChannelID string
	Format    ItemFormat
	Planned   bool
	// Counts holds the statuses with a count above zero, in lifecycle order.
	Counts []StatusCount
	Total  int
}

// StatusCount is how many items are in one status.
type StatusCount struct {
	Status ItemStatus
	Count  int
}

// ItemCount is one (channel, format, status) count of a campaign's items:
// the store's coverage aggregate.
type ItemCount struct {
	ChannelID string
	Format    ItemFormat
	Status    ItemStatus
	Count     int
}

// Asset is one file attached to an item or a campaign: metadata only.
type Asset struct {
	ID    string
	OrgID string
	// ItemID and CampaignID: at least one is set.
	ItemID     string
	CampaignID string
	// ObjectKey is gtm/<org_id>/<id> in the org's bucket.
	ObjectKey string
	Filename  string
	// ContentType is the sniffed type once READY, the declared one before.
	ContentType string
	SizeBytes   int64
	// SHA256 is hex, set once READY.
	SHA256     string
	Status     AssetStatus
	UploadedBy string
	Source     AssetSource
	CreatedAt  time.Time
}

// AssetUpload is a started upload: the PENDING asset and where to PUT it.
type AssetUpload struct {
	Asset           Asset
	UploadURL       string
	RequiredHeaders map[string]string
	ExpiresAt       time.Time
}

// AssetDownload is a short-lived URL for a READY asset's bytes.
type AssetDownload struct {
	URL       string
	ExpiresAt time.Time
}

// LibraryEntry is one entry of the embedded idea bank (internal/gtm/library),
// in domain terms: category, business types and models are the proto enum
// names. Text fields are markdown; an empty BusinessTypes, BusinessModels
// or Stages means any. The bank is the same, read-only, for every org.
type LibraryEntry struct {
	Key            string
	Category       StrategyCategory
	Name           string
	SummaryMD      string
	WhyItWorksMD   string
	BusinessTypes  []BusinessType
	BusinessModels []BusinessModel
	// Stages are bank stage slugs (library.Stages); Objective, Effort and
	// Cost are bank slugs too.
	Stages    []string
	Objective string
	Effort    string
	Cost      string
	// SignalDays is the days to a first signal; 0 when the entry does not
	// say.
	SignalDays int
	// TailorQuestions are what an agent answers for one business
	// (Idea.TailorAnswers).
	TailorQuestions     []string
	SuggestedWorkflowMD string
	// ChannelTypes are channel-type slugs, matched against the org's
	// channels by UseLibraryStrategy.
	ChannelTypes []string
	KPIs         []string
}

// Limits of the r2/r3 surface.
const (
	DefaultStrategyPageSize = 50
	MaxStrategyPageSize     = 200
	// MaxStrategyCampaigns caps GetStrategy's campaigns.
	MaxStrategyCampaigns   = 200
	DefaultAssetPageSize   = 50
	MaxAssetPageSize       = 200
	DefaultLibraryPageSize = 50
	MaxLibraryPageSize     = 200
	// MaxPlannedChannels caps a campaign's planned channels.
	MaxPlannedChannels = 50
	// MaxChannels caps an org's channels (they are read unpaged).
	MaxChannels = 200

	// MaxAssetBytes caps an uploaded asset; MaxCopiedAssetBytes one copied
	// from a chat attachment; MaxOrgAssetBytes an org's assets together
	// (READY and PENDING).
	MaxAssetBytes       int64 = 25 << 20
	MaxCopiedAssetBytes int64 = 64 << 20
	MaxOrgAssetBytes    int64 = 2 << 30

	// AssetUploadTTL is the longest an upload's presigned PUT stays valid.
	// A PENDING asset counts against the quota, and can be confirmed, until
	// AssetUploadTTL plus a few minutes' grace after its creation; then it
	// has expired, and the org's next upload or confirm deletes it.
	AssetUploadTTL = 15 * time.Minute
)

// ListStrategiesInput is the input to Service.ListStrategies.
type ListStrategiesInput struct {
	Status    StrategyStatus
	PageSize  int
	PageToken string
}

// ListStrategiesResult is the result of Service.ListStrategies.
type ListStrategiesResult struct {
	Strategies    []Strategy
	NextPageToken string
}

// GetStrategyInput is the input to Service.GetStrategy.
type GetStrategyInput struct {
	ID string
}

// CreateStrategyInput is the input to Service.CreateStrategy.
type CreateStrategyInput struct {
	Name         string
	GoalMD       string
	TargetMetric string
	// PeriodStart and PeriodEnd: YYYY-MM-DD or "".
	PeriodStart string
	PeriodEnd   string
	// Status "" is DRAFT; an agent creates DRAFT only.
	Status StrategyStatus
	// Category "" is none.
	Category StrategyCategory
}

// UpdateStrategyInput is the input to Service.UpdateStrategy. A nil field
// is left alone.
type UpdateStrategyInput struct {
	ID               string
	ExpectedRevision int32
	Name             *string
	GoalMD           *string
	TargetMetric     *string
	// PeriodStart and PeriodEnd, when set: YYYY-MM-DD, or "" to clear.
	PeriodStart *string
	PeriodEnd   *string
	// Status is a person's act only.
	Status *StrategyStatus
	// Category, when set: the category, or "" to clear.
	Category *StrategyCategory
}

// ListChannelsInput is the input to Service.ListChannels.
type ListChannelsInput struct {
	Status ChannelStatus
	Mode   ChannelMode
}

// ListChannelsResult is the result of Service.ListChannels: every channel
// of the org that matches, by key.
type ListChannelsResult struct {
	Channels []Channel
}

// GetChannelInput is the input to Service.GetChannel.
type GetChannelInput struct {
	ID string
}

// CreateChannelInput is the input to Service.CreateChannel.
type CreateChannelInput struct {
	Key  string
	Name string
	// Mode is required. An agent creates MANUAL only.
	Mode ChannelMode
	// IntegrationKind is required for INTEGRATED and refused for MANUAL;
	// ConnectionID is INTEGRATED only ("" is the user's default).
	IntegrationKind      string
	ConnectionID         string
	ManualInstructionsMD string
	ChannelTypes         []string
	// Status "" is ACTIVE.
	Status ChannelStatus
}

// UpdateChannelInput is the input to Service.UpdateChannel. A nil field is
// left alone; an empty IntegrationKind or ConnectionID clears it.
type UpdateChannelInput struct {
	ID               string
	ExpectedRevision int32
	// Key, when set, renames the channel's slug; one the org holds is
	// ErrChannelExists.
	Key                  *string
	Name                 *string
	Mode                 *ChannelMode
	IntegrationKind      *string
	ConnectionID         *string
	ManualInstructionsMD *string
	// ChannelTypes, when non-nil, replaces the list.
	ChannelTypes *[]string
	Status       *ChannelStatus
}

// ListAssetsInput is the input to Service.ListAssets: exactly one of ItemID
// and CampaignID.
type ListAssetsInput struct {
	ItemID     string
	CampaignID string
	PageSize   int
	PageToken  string
}

// ListAssetsResult is the result of Service.ListAssets.
type ListAssetsResult struct {
	Assets        []Asset
	NextPageToken string
}

// GetAssetInput is the input to Service.GetAsset.
type GetAssetInput struct {
	ID string
}

// CreateAssetUploadInput is the input to Service.CreateAssetUpload: exactly
// one of ItemID and CampaignID.
type CreateAssetUploadInput struct {
	ItemID      string
	CampaignID  string
	Filename    string
	ContentType string
	SizeBytes   int64
}

// ConfirmAssetUploadInput is the input to Service.ConfirmAssetUpload.
type ConfirmAssetUploadInput struct {
	AssetID string
}

// GetAssetDownloadInput is the input to Service.GetAssetDownload.
type GetAssetDownloadInput struct {
	AssetID string
}

// DeleteAssetInput is the input to Service.DeleteAsset.
type DeleteAssetInput struct {
	AssetID string
}

// AttachFromAttachmentInput is the input to Service.AttachFromAttachment:
// a chat attachment of the actor's, and exactly one of ItemID and
// CampaignID.
type AttachFromAttachmentInput struct {
	AttachmentID string
	ItemID       string
	CampaignID   string
}

// ListStrategyLibraryInput is the input to Service.ListStrategyLibrary.
// Every empty filter matches anything; set ones combine.
type ListStrategyLibraryInput struct {
	// Categories: the entry's category is one of these. Each list is
	// capped as the proto caps it (MaxLibraryCategories,
	// MaxLibraryBusinessTypes, MaxLibraryBusinessModels; past that is
	// InvalidArgument) and a repeated value counts once.
	Categories []StrategyCategory
	// BusinessTypes, BusinessModels: the entry fits any, or one of these.
	BusinessTypes  []BusinessType
	BusinessModels []BusinessModel
	// Stage: the entry fits any stage, or this one. Stage, Objective,
	// Effort and Cost are bank slugs (library.Stages, ...).
	Stage     string
	Objective string
	Effort    string
	Cost      string
	// Query: every term appears in the name or summary, case-insensitively.
	Query string
	// FitsMyBusiness keeps the entries that fit the org's positioning
	// (business_type, business_models; library.Fits): an unset side of the
	// profile excludes nothing.
	FitsMyBusiness bool
	// Diverse returns a spread instead of a page: up to PageSize entries (0
	// is DefaultLibraryPageSize) round-robin across the matching
	// categories (library.Diverse). PageToken is ignored and NextPageToken
	// is empty.
	Diverse   bool
	PageSize  int
	PageToken string
}

// ListStrategyLibraryResult is the result of Service.ListStrategyLibrary.
type ListStrategyLibraryResult struct {
	Entries       []LibraryEntry
	NextPageToken string
}

// GetStrategyLibraryEntryInput is the input to Service.GetStrategyLibraryEntry.
type GetStrategyLibraryEntryInput struct {
	Key string
}

// UseLibraryStrategyInput is the input to Service.UseLibraryStrategy.
type UseLibraryStrategyInput struct {
	LibraryKey          string
	CreateFirstCampaign bool
	// CampaignName is the first campaign's; nil is the entry's name.
	CampaignName *string
}

// UseLibraryStrategyResult is the result of Service.UseLibraryStrategy.
type UseLibraryStrategyResult struct {
	Strategy Strategy
	// Campaign is nil when none was asked for.
	Campaign *Campaign
}

// Store filters for the r2/r3 tables (see IdeaFilter).

// StrategyFilter selects strategies for Store.ListStrategies.
type StrategyFilter struct {
	OrgID  string
	Status StrategyStatus
	Limit  int
	Offset int
}

// ChannelFilter selects channels for Store.ListChannels.
type ChannelFilter struct {
	OrgID  string
	Status ChannelStatus
	Mode   ChannelMode
	Limit  int
}

// AssetFilter selects assets for Store.ListAssets.
type AssetFilter struct {
	OrgID      string
	ItemID     string
	CampaignID string
	Limit      int
	Offset     int
}

// Asset storage (PR-13 implements AssetStore over pkg/blobstore).

// PresignedRequest is a presigned HTTP request on the bucket: the URL, the
// headers it must carry exactly, and when it stops working.
type PresignedRequest struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// PutOptions condition a write: the object's content type and exact size.
type PutOptions struct {
	ContentType string
	Size        int64
	// TTL, for PresignPut, is the longest the URL may stay valid: its
	// ExpiresAt is at most now + TTL. Put ignores it.
	TTL time.Duration
}

// GetOptions shape a presigned read: inline (shown in the browser) or an
// attachment (downloaded, under Filename), with ContentType as the
// response's type.
type GetOptions struct {
	Inline      bool
	ContentType string
	Filename    string
}

// AttachmentFile describes a chat attachment.
type AttachmentFile struct {
	Filename    string
	ContentType string
	// Size is the attachment's size in bytes, as the attachment store has it.
	Size int64
}
