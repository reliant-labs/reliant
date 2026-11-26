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

// LibraryDepth is how much a library entry holds.
type LibraryDepth string

// LibraryDepth values.
const (
	DepthPlay   LibraryDepth = "GTM_LIBRARY_DEPTH_PLAY"
	DepthTactic LibraryDepth = "GTM_LIBRARY_DEPTH_TACTIC"
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

// LibraryStep is one step of a play and who does it.
type LibraryStep struct {
	Who   string `json:"who"`
	Title string `json:"title"`
}

// LibraryEntry is one play or tactic of an org's strategy library, as the
// org imported it. Reliant ships none.
type LibraryEntry struct {
	Key          string
	Category     StrategyCategory
	Depth        LibraryDepth
	Name         string
	SummaryMD    string
	Icon         string
	Stage        string
	Cost         string
	CostNote     string
	Difficulty   string
	Level        string
	Objective    string
	NorthStar    string
	HypothesisMD string
	// Hours, SignalDays, WindowDays and LaunchDays are 0 when the entry
	// does not say.
	Hours         int32
	SignalDays    int32
	WindowDays    int32
	LaunchDays    int32
	ExpectedDelta float64
	Lifecycle     []string
	ChannelTypes  []string
	Integrations  []string
	Fits          []string
	Prerequisites []string
	KPIs          []string
	Tactics       []string
	Automates     []string
	Experiments   []string
	StopWhen      []string
	Steps         []LibraryStep
	// Extra is every field of the entry the columns have no place for.
	Extra map[string]any
	// Set by the import; ignored on the way in.
	SourceRevision string
	ImportedAt     time.Time
	ImportedBy     string
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
	// MaxLibraryImport caps one ImportStrategyLibrary batch.
	MaxLibraryImport = 500
	// MaxLibraryKeys caps ImportStrategyLibrary's AllKeys.
	MaxLibraryKeys = 5000
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

// AttachFromAttachmentInput is the input to Service.AttachFromAttachment:
// a chat attachment of the actor's, and exactly one of ItemID and
// CampaignID.
type AttachFromAttachmentInput struct {
	AttachmentID string
	ItemID       string
	CampaignID   string
}

// ListStrategyLibraryInput is the input to Service.ListStrategyLibrary.
// Every "" filter matches anything.
type ListStrategyLibraryInput struct {
	Category   StrategyCategory
	Depth      LibraryDepth
	Stage      string
	Cost       string
	Difficulty string
	// Fit matches entries whose fits include it.
	Fit string
	// Query matches name and summary, case-insensitively.
	Query     string
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

// ImportStrategyLibraryInput is the input to Service.ImportStrategyLibrary.
type ImportStrategyLibraryInput struct {
	// Entries: 1 to MaxLibraryImport, each key once.
	Entries []LibraryEntry
	// SourceRevision is what they were exported from (a commit SHA).
	SourceRevision string
	// Prune removes the org's entries whose key is not in AllKeys.
	Prune bool
	// AllKeys is every key of the whole library being imported (at most
	// MaxLibraryKeys), across all its batches; required with Prune, and
	// every entry's key must be in it.
	AllKeys []string
}

// ImportStrategyLibraryResult counts what an import did.
type ImportStrategyLibraryResult struct {
	Created   int
	Updated   int
	Unchanged int
	Pruned    int
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

// LibraryFilter selects library entries for Store.ListLibrary.
type LibraryFilter struct {
	OrgID      string
	Category   StrategyCategory
	Depth      LibraryDepth
	Stage      string
	Cost       string
	Difficulty string
	Fit        string
	Query      string
	// Keys, when non-nil, selects exactly these keys.
	Keys   []string
	Limit  int
	Offset int
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
