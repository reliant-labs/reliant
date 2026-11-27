// Copyright (c) 2026 Reliant Labs

// Package gtm is the GTM domain: an org's strategies and the campaigns under
// them, ideas, items (posts, emails, videos: the calendar) in a format and on
// a channel, the assets attached to items and campaigns, positioning and
// claims registry, and the strategy library (the embedded, read-only idea
// bank of internal/gtm/library). Both the Connect
// handler (internal/handlers/gtm) and the agent tools call it, so access and
// validation are decided here, once.
//
// Every method applies the same rules, in this order:
//
//  1. The actor's org must be entitled to GTM and the actor a live member of
//     it (featuregate.GTM). Anything else, a gate outage included, is
//     ErrNotFound: a caller outside GTM cannot tell it exists.
//  2. Every row is read and written WHERE org_id = the actor's org. A link to
//     a campaign or idea is checked to be the same org inside the write's
//     transaction (and the composite foreign keys hold it too).
//  3. A write names the revision it read; a row that moved on is a
//     *RevisionConflictError carrying the current revision.
//  4. Only a Person moves an idea to ACCEPTED or REJECTED, approves,
//     schedules, publishes or pauses an item, or verifies or retires a
//     claim. An Agent creates and edits PROPOSED ideas, DRAFT / REVIEW items
//     (content, status between the two, and scheduled_at, which records a
//     date and never publishes) and UNVERIFIED claims, and touches nothing
//     else. The item lifecycle is checkTransition's. An agent creates and
//     edits DRAFT strategies and MANUAL channels; integrations, strategy
//     status and published URLs are a person's.
//  5. Nothing here publishes. SENT is written only by the approved email
//     path: SendItemEmail, or a person's ResolveInDoubtSend for a send that
//     left without its record.
//
// Built by internal/app's GTM provider (PR-12), which wires the gate, the
// store over the ORM (NewStore) and the analytics and email clients.
//
//forge:provided
package gtm

import (
	"context"
	"io"
	"time"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/store"
)

// Service is the GTM domain boundary. Every method takes the Actor it acts
// for; see the package doc for the rules each applies.
type Service interface {
	// GetAccess reports whether the actor may use GTM at all. A refusal, and a
	// gate outage, are Enabled false with a nil error.
	GetAccess(ctx context.Context, actor Actor) (Access, error)

	// ListIdeas lists the org's ideas, newest first.
	ListIdeas(ctx context.Context, actor Actor, in ListIdeasInput) (ListIdeasResult, error)
	// GetIdea returns one idea.
	GetIdea(ctx context.Context, actor Actor, in GetIdeaInput) (Idea, error)
	// CreateIdea creates a PROPOSED idea. An agent's idea has source AGENT and
	// the actor's chat as its source chat. An idea drawn from the idea bank
	// names its entry (LibraryKey: a key the bank does not hold is
	// InvalidArgument) and answers its tailor questions (TailorAnswers).
	CreateIdea(ctx context.Context, actor Actor, in CreateIdeaInput) (Idea, error)
	// UpdateIdea changes an idea's fields. An agent edits PROPOSED ideas only.
	UpdateIdea(ctx context.Context, actor Actor, in UpdateIdeaInput) (Idea, error)
	// SetIdeaStatus moves an idea to a status. ACCEPTED and REJECTED are a
	// person's call, and an agent changes no idea's status.
	SetIdeaStatus(ctx context.Context, actor Actor, in SetIdeaStatusInput) (Idea, error)

	// ListCampaigns lists the org's campaigns, newest first.
	ListCampaigns(ctx context.Context, actor Actor, in ListCampaignsInput) (ListCampaignsResult, error)
	// GetCampaign returns a campaign with the ideas and items linked to it,
	// at most MaxCampaignIdeas / MaxCampaignItems of each (Truncated says
	// when either was cut).
	GetCampaign(ctx context.Context, actor Actor, in GetCampaignInput) (CampaignDetail, error)
	// CreateCampaign creates a campaign (DRAFT unless a status is given).
	CreateCampaign(ctx context.Context, actor Actor, in CreateCampaignInput) (Campaign, error)
	// UpdateCampaign changes a campaign's fields.
	UpdateCampaign(ctx context.Context, actor Actor, in UpdateCampaignInput) (Campaign, error)

	// ListItems lists the org's items: scheduled ones soonest first, then the
	// rest newest first. A scheduled range makes it the calendar.
	ListItems(ctx context.Context, actor Actor, in ListItemsInput) (ListItemsResult, error)
	// GetItem returns one item.
	GetItem(ctx context.Context, actor Actor, in GetItemInput) (Item, error)
	// CreateItem creates a DRAFT or REVIEW item (unset: DRAFT).
	CreateItem(ctx context.Context, actor Actor, in CreateItemInput) (Item, error)
	// UpdateItem changes an item's fields and status along the lifecycle
	// (DRAFT <-> REVIEW; a person: -> APPROVED, APPROVED -> SCHEDULED and
	// APPROVED/SCHEDULED -> PUBLISHED for non-emails, any -> PAUSED,
	// PAUSED -> DRAFT); a refused move is a *TransitionError. A content
	// change to an APPROVED or SCHEDULED item drops it to REVIEW. SENT is
	// never set here, and a SENT item is final. A held item (a send in
	// doubt) is refused whole (*SendInDoubtError) until ResolveInDoubtSend.
	// An agent touches DRAFT and REVIEW items only.
	UpdateItem(ctx context.Context, actor Actor, in UpdateItemInput) (Item, error)
	// SendItemEmail sends an APPROVED email item, at exactly the revision
	// named, through Email as the actor with the run's credential, then
	// records it SENT. The approval hash must be ApprovalHash of the item as
	// stored now, so the person approved exactly what is sent. The message is
	// built from the stored item, never from the caller. When the item's
	// channel names a Gmail connection, the send goes through that one: a
	// run naming another is ErrChannelNotSendable. A send that may
	// have left without its record landing holds the item
	// (*SendInDoubtError, *SentUnrecordedError) rather than risk a second
	// email; see the implementation for the steps.
	SendItemEmail(ctx context.Context, actor Actor, in SendItemEmailInput) (Item, error)
	// ResolveInDoubtSend settles a held item: a person's call, after checking
	// the sent mail in Gmail. The approval hash must be the hold's. With a
	// Gmail message id (the person's manual marker, stored unchecked) it
	// records the item SENT. Without one it releases the hold (the email did
	// not go out): refused (*SendInFlightError) while the hold is younger than
	// twice EmailSendTimeout, and it advances the revision so every earlier
	// approval card is void. An item that is not held is ErrNotHeld.
	ResolveInDoubtSend(ctx context.Context, actor Actor, in ResolveInDoubtSendInput) (Item, error)

	// GetPositioning returns the org's positioning; revision 0 when it has none.
	GetPositioning(ctx context.Context, actor Actor) (Positioning, error)
	// UpdatePositioning replaces the org's positioning whole, its business
	// type and models included (what FitsMyBusiness reads). Expected
	// revision 0 creates it.
	UpdatePositioning(ctx context.Context, actor Actor, in UpdatePositioningInput) (Positioning, error)

	// ListClaims lists the org's claims registry.
	ListClaims(ctx context.Context, actor Actor, in ListClaimsInput) (ListClaimsResult, error)
	// UpsertClaim creates a claim (no ID) or changes one. VERIFIED (with a
	// source and the person as verifier) and RETIRED are a person's call; an
	// agent creates and edits UNVERIFIED claims only. Text and category never
	// change, and a text the org holds is ErrClaimExists.
	UpsertClaim(ctx context.Context, actor Actor, in UpsertClaimInput) (UpsertClaimResult, error)
	// CheckClaims checks a text against the org's claims (claimcheck). It
	// stores nothing.
	CheckClaims(ctx context.Context, actor Actor, in CheckClaimsInput) (CheckClaimsResult, error)

	// ReadProductAnalytics runs one aggregate product-analytics template over
	// the actor's org's product events, through Analytics.
	ReadProductAnalytics(ctx context.Context, actor Actor, q AnalyticsQuery) (AnalyticsResult, error)

	// ListStrategies lists the org's strategies, newest first.
	ListStrategies(ctx context.Context, actor Actor, in ListStrategiesInput) (ListStrategiesResult, error)
	// GetStrategy returns a strategy with its campaigns (capped, Truncated).
	GetStrategy(ctx context.Context, actor Actor, in GetStrategyInput) (StrategyDetail, error)
	// CreateStrategy creates a strategy, DRAFT unless a person says otherwise.
	CreateStrategy(ctx context.Context, actor Actor, in CreateStrategyInput) (Strategy, error)
	// UpdateStrategy edits a strategy or moves its status. An agent edits
	// DRAFT strategies only and never changes a status.
	UpdateStrategy(ctx context.Context, actor Actor, in UpdateStrategyInput) (Strategy, error)

	// ListChannels lists the org's channels by key, seeding the defaults
	// (DefaultChannels()) for an org that has none, idempotently.
	ListChannels(ctx context.Context, actor Actor, in ListChannelsInput) (ListChannelsResult, error)
	// GetChannel returns one channel, seeding as ListChannels does.
	GetChannel(ctx context.Context, actor Actor, in GetChannelInput) (Channel, error)
	// CreateChannel creates a channel. An agent creates MANUAL channels
	// only; a key the org holds is ErrChannelExists, and a channel past
	// MaxChannels is ErrTooManyChannels.
	CreateChannel(ctx context.Context, actor Actor, in CreateChannelInput) (Channel, error)
	// UpdateChannel edits a channel. An agent changes name,
	// manual_instructions_md and status only, and never an integration.
	UpdateChannel(ctx context.Context, actor Actor, in UpdateChannelInput) (Channel, error)

	// ListAssets lists the assets of an item or a campaign, newest first.
	ListAssets(ctx context.Context, actor Actor, in ListAssetsInput) (ListAssetsResult, error)
	// GetAsset returns one asset's metadata.
	GetAsset(ctx context.Context, actor Actor, in GetAssetInput) (Asset, error)
	// CreateAssetUpload records a PENDING asset and returns a presigned PUT
	// of its bytes to a staging object, valid for at most AssetUploadTTL
	// (MaxAssetBytes; MaxOrgAssetBytes per org, counting READY assets and
	// PENDING ones that have not expired).
	CreateAssetUpload(ctx context.Context, actor Actor, in CreateAssetUploadInput) (AssetUpload, error)
	// ConfirmAssetUpload reads the staging object once, checks those bytes
	// (size, sniffed type, sha256), marks the asset READY and writes exactly
	// them to the asset's own key; a refusal deletes both objects and the
	// row. An expired PENDING asset is ErrAssetUploadExpired, and deleted.
	ConfirmAssetUpload(ctx context.Context, actor Actor, in ConfirmAssetUploadInput) (Asset, error)
	// GetAssetDownload returns a short-lived URL for a READY asset: inline
	// for raster images and mp4/webm, an attachment otherwise. A READY row
	// whose object is missing is ErrAssetNotReady.
	GetAssetDownload(ctx context.Context, actor Actor, in GetAssetDownloadInput) (AssetDownload, error)
	// DeleteAsset deletes an asset, in any status: its row, then its
	// objects (final and staging). A person's call.
	DeleteAsset(ctx context.Context, actor Actor, in DeleteAssetInput) error
	// AttachFromAttachment copies one of the actor's chat attachments into
	// a READY asset of an item or a campaign (MaxCopiedAssetBytes), with the
	// same checks as ConfirmAssetUpload. PR-7's attach_gtm_asset calls it.
	AttachFromAttachment(ctx context.Context, actor Actor, in AttachFromAttachmentInput) (Asset, error)

	// ListStrategyLibrary lists the strategy library, the embedded idea
	// bank (internal/gtm/library; read-only, the same for every org): by
	// category then name, paged, or with Diverse a spread across
	// categories. FitsMyBusiness keeps the entries that fit the org's
	// positioning (business_type, business_models).
	ListStrategyLibrary(ctx context.Context, actor Actor, in ListStrategyLibraryInput) (ListStrategyLibraryResult, error)
	// GetStrategyLibraryEntry returns one bank entry by key; a key the bank
	// does not hold is ErrNotFound.
	GetStrategyLibraryEntry(ctx context.Context, actor Actor, in GetStrategyLibraryEntryInput) (LibraryEntry, error)
	// UseLibraryStrategy makes a DRAFT strategy from a bank entry, and
	// optionally a first DRAFT campaign planning the matching channels.
	UseLibraryStrategy(ctx context.Context, actor Actor, in UseLibraryStrategyInput) (UseLibraryStrategyResult, error)
}

// Connections reports whether the actor can reach an integration now, for
// an INTEGRATED channel's connection_state (PR-12 wires it to Reliant's
// integration connections). Display only: an error is UNKNOWN, and nothing
// is ever authorized on it.
type Connections interface {
	// State is the state of connectionID ("" is the actor's default
	// connection for integrationKind), for actor.UserID.
	State(ctx context.Context, actor Actor, integrationKind, connectionID string) (ConnectionState, error)
}

// AssetStore holds asset bytes in the org's bucket (PR-13, over
// pkg/blobstore). The Service builds every key, and an implementation never
// takes a key from a client. An asset has two:
//
//   - gtm/<org_id>/<asset_id>.upload, the staging object: the only key a
//     client ever writes, through PresignPut.
//   - gtm/<org_id>/<asset_id>, the asset's own key: written only
//     server-side, by Put, with bytes the Service read and checked. It is
//     never presigned for a PUT, so what is served is what was checked.
type AssetStore interface {
	// PresignPut returns a presigned PUT of exactly opts.Size bytes of
	// opts.ContentType to key, expiring no later than now + opts.TTL.
	PresignPut(ctx context.Context, key string, opts PutOptions) (PresignedRequest, error)
	// Open reads key; a missing object is ErrObjectNotFound.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes key from r, which carries exactly opts.Size bytes.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
	// Delete removes key; a missing object is not an error.
	Delete(ctx context.Context, key string) error
	// Stat reports key's size, without reading it (a HEAD); a missing
	// object is ErrObjectNotFound. GetAssetDownload asks it before
	// presigning, so a READY row whose bytes are gone is not served.
	Stat(ctx context.Context, key string) (size int64, err error)
	// PresignGet returns a presigned GET of key, shaped by opts. Every
	// response it signs MUST carry Content-Type opts.ContentType, and
	// X-Content-Type-Options: nosniff where the backend supports it (an
	// S3-style presigned GET overrides only the standard response headers;
	// there the Content-Type override and the attachment disposition carry
	// the guarantee), and a
	// Content-Disposition (inline or
	// attachment, per opts.Inline) whose filename is opts.Filename encoded
	// per RFC 6266 (an ASCII filename= fallback and filename*=UTF-8''
	// percent-encoded), so no stored name can break out of the header.
	PresignGet(ctx context.Context, key string, opts GetOptions) (PresignedRequest, error)
}

// Attachments reads Reliant chat attachments for AttachFromAttachment (PR-12
// wires it). An implementation MUST be owner-scoped: an attachment that is
// not actor.UserID's own, or whose chat is not in actor.OrgID, is
// ErrNotFound, indistinguishable from a missing one.
type Attachments interface {
	// Open returns the attachment's description and its bytes.
	Open(ctx context.Context, actor Actor, attachmentID string) (AttachmentFile, io.ReadCloser, error)
}

// Gate is the GTM entitlement check this package makes: the part of
// featuregate.Gate it calls. featuregate.Gate satisfies it.
type Gate interface {
	// Allowed reports whether userID is a live member of orgID and orgID is
	// entitled to f. An error is a refusal.
	Allowed(ctx context.Context, f featuregate.Feature, userID, orgID string) (bool, error)
}

// Clock is the time source; tests supply a fixed one.
type Clock interface {
	Now() time.Time
}

// Analytics runs product-analytics templates against control-plane's product
// events (internal/gtm/analytics). It is handed a query the Service already
// gated, validated (AnalyticsQuery.Validate) and resolved (no zero window or
// limit), and the org the run belongs to: it reads that org's product events
// only, never another org's, and never traces, logs or metrics.
//
// It builds every query itself from the template: a query never carries SQL,
// and names (events, the property) are emitted as escaped literals. Results
// are aggregates only, never a user id or any other identifier.
//
// ReadProductAnalytics passes three kinds of error through to the caller,
// message and all: one that wraps ErrAnalyticsFailed, one that wraps
// ErrAnalyticsUnavailable, and one that wraps a *ValidationError (the only
// ErrInvalidArgument that passes). An implementation MUST put only fixed
// text in those, its own wrappers and a ValidationError's Field and Reason
// included: never a cause's text, a host, a query or a value it read.
// Anything else, a bare svcerr.InvalidArgument included, is logged and
// becomes ErrAnalyticsFailed.
type Analytics interface {
	// Query runs q over orgID's product events.
	Query(ctx context.Context, orgID string, q AnalyticsQuery) (AnalyticsResult, error)
}

// Email sends one email through Gmail as the actor (internal/gtm/email). It
// is called only by SendItemEmail, for an approved item, with the actor the
// Service admitted.
//
// An implementation MUST fail closed, sending nothing, unless the run and
// the Gmail connection it would send through belong to actor.UserID and
// actor.OrgID: the run's recorded owner is actor.UserID and its org is
// actor.OrgID, and the connection (run.ConnectionID, or the owner's default)
// is that user's own. The credential is resolved from the run record
// (connauth), never from the message or the run fields alone.
type Email interface {
	// Send sends msg and returns Gmail's message id. An error that wraps
	// ErrEmailNotSent says the message certainly did not leave (a refused
	// run or connection, no connection, a request Gmail rejected); any
	// other error leaves the send in doubt, and the item is held until a
	// person resolves it. The error's text is logged, never shown.
	//
	// Send MUST return by ctx's deadline (EmailSendTimeout), abandoning any
	// request still running. ResolveInDoubtSend lets a person release a
	// hold once it is twice EmailSendTimeout old, on the premise that no
	// Send of it can still be running by then; a Send that outlives its
	// deadline could deliver after that release, and a new approval would
	// then send a second email. The hold's age is the Service clock's now
	// less the hold's updated_at, which the Service stamps from that same
	// clock, so every replica's clock must be within a small fraction of
	// EmailSendTimeout of true time (NTP-synced hosts are).
	Send(ctx context.Context, actor Actor, run EmailRun, msg EmailMessage) (gmailMessageID string, err error)
}

// Store is GTM's persistence, over the generated internal/store entities.
// NewStore is the implementation over the ORM.
//
// Every read and write is scoped to an org: a row of another org is absent.
// A missing row is ErrNotFound. Writes report a link to a row the org does
// not have as a *LinkError, a violated CHECK as a *ValidationError, and a
// claim text the org already holds as ErrClaimExists.
type Store interface {
	// InTx runs fn in one SERIALIZABLE transaction, re-run on a serialization
	// conflict: fn may run more than once and must do nothing outside the
	// database. Store calls with the ctx fn receives join it.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error

	// ListIdeas returns the ideas f matches, newest first.
	ListIdeas(ctx context.Context, f IdeaFilter) ([]*store.GtmIdea, error)
	// GetIdea returns orgID's idea id.
	GetIdea(ctx context.Context, orgID, id string) (*store.GtmIdea, error)
	// CreateIdea inserts row, assigning its ID when empty.
	CreateIdea(ctx context.Context, row *store.GtmIdea) error
	// UpdateIdea writes row's mutable columns and revision expected+1 where
	// the stored row is at expected; one that is not is ErrRevisionConflict.
	UpdateIdea(ctx context.Context, row *store.GtmIdea, expected int32) error

	// ListCampaigns returns the campaigns f matches, newest first.
	ListCampaigns(ctx context.Context, f CampaignFilter) ([]*store.GtmCampaign, error)
	// GetCampaign returns orgID's campaign id.
	GetCampaign(ctx context.Context, orgID, id string) (*store.GtmCampaign, error)
	// CreateCampaign inserts row, assigning its ID when empty.
	CreateCampaign(ctx context.Context, row *store.GtmCampaign) error
	// UpdateCampaign is UpdateIdea for a campaign.
	UpdateCampaign(ctx context.Context, row *store.GtmCampaign, expected int32) error

	// ListItems returns the items f matches: scheduled ones soonest first,
	// then the rest newest first.
	ListItems(ctx context.Context, f ItemFilter) ([]*store.GtmItem, error)
	// GetItem returns orgID's item id.
	GetItem(ctx context.Context, orgID, id string) (*store.GtmItem, error)
	// HoldItemSend puts a send hold on an APPROVED, unheld item at revision:
	// approval_hash = hash and updated_at = at (the hold's time), WITHOUT
	// advancing the revision (the approval card's hash covers it). A
	// non-empty approval_hash on an item that is not SENT is a send in flight
	// or in doubt. A row not in that state is ErrRevisionConflict.
	HoldItemSend(ctx context.Context, orgID, id string, revision int32, hash string, at time.Time) error
	// ReleaseItemSend lifts the hold `hash` from an APPROVED item at
	// revision: approval_hash = '' and updated_at = at. With invalidate it
	// also advances the revision, so every card issued before stops
	// matching ApprovalHash. A row not in that state is ErrRevisionConflict.
	ReleaseItemSend(ctx context.Context, orgID, id string, revision int32, hash string, at time.Time, invalidate bool) error
	// CreateItem inserts row, assigning its ID when empty.
	CreateItem(ctx context.Context, row *store.GtmItem) error
	// UpdateItem is UpdateIdea for an item.
	UpdateItem(ctx context.Context, row *store.GtmItem, expected int32) error

	// GetPositioning returns orgID's positioning.
	GetPositioning(ctx context.Context, orgID string) (*store.GtmPositioning, error)
	// CreatePositioning inserts orgID's first positioning; one that exists is
	// ErrRevisionConflict.
	CreatePositioning(ctx context.Context, row *store.GtmPositioning) error
	// UpdatePositioning is UpdateIdea for the positioning.
	UpdatePositioning(ctx context.Context, row *store.GtmPositioning, expected int32) error

	// ListClaims returns the claims f matches, newest first.
	ListClaims(ctx context.Context, f ClaimFilter) ([]*store.GtmClaim, error)
	// GetClaim returns orgID's claim id.
	GetClaim(ctx context.Context, orgID, id string) (*store.GtmClaim, error)
	// CreateClaim inserts row, assigning its ID when empty.
	CreateClaim(ctx context.Context, row *store.GtmClaim) error
	// UpdateClaim is UpdateIdea for a claim; text and category are never
	// written.
	UpdateClaim(ctx context.Context, row *store.GtmClaim, expected int32) error

	// ListStrategies returns the strategies f matches, newest first.
	ListStrategies(ctx context.Context, f StrategyFilter) ([]*store.GtmStrategy, error)
	// GetStrategy returns orgID's strategy id.
	GetStrategy(ctx context.Context, orgID, id string) (*store.GtmStrategy, error)
	// CreateStrategy inserts row, assigning its ID when empty.
	CreateStrategy(ctx context.Context, row *store.GtmStrategy) error
	// UpdateStrategy is UpdateIdea for a strategy.
	UpdateStrategy(ctx context.Context, row *store.GtmStrategy, expected int32) error

	// ListChannels returns the channels f matches, by key.
	ListChannels(ctx context.Context, f ChannelFilter) ([]*store.GtmChannel, error)
	// CountChannels counts orgID's channels.
	CountChannels(ctx context.Context, orgID string) (int64, error)
	// GetChannel returns orgID's channel id.
	GetChannel(ctx context.Context, orgID, id string) (*store.GtmChannel, error)
	// SeedChannels inserts rows, each skipped when its org already holds its
	// key (ON CONFLICT (org_id, key) DO NOTHING).
	SeedChannels(ctx context.Context, rows []*store.GtmChannel) error
	// CreateChannel inserts row; a key the org holds is ErrChannelExists.
	CreateChannel(ctx context.Context, row *store.GtmChannel) error
	// UpdateChannel is UpdateIdea for a channel (its key is never written).
	UpdateChannel(ctx context.Context, row *store.GtmChannel, expected int32) error

	// CampaignItemCounts counts orgID's campaign's items by channel, format
	// and status: every item, not a page.
	CampaignItemCounts(ctx context.Context, orgID, campaignID string) ([]ItemCount, error)

	// ListAssets returns the assets f matches, newest first.
	ListAssets(ctx context.Context, f AssetFilter) ([]*store.GtmAsset, error)
	// GetAsset returns orgID's asset id.
	GetAsset(ctx context.Context, orgID, id string) (*store.GtmAsset, error)
	// SumAssetBytes is the size of orgID's READY assets and of its PENDING
	// ones created after pendingAfter (those not yet expired).
	SumAssetBytes(ctx context.Context, orgID string, pendingAfter time.Time) (int64, error)
	// ListExpiredAssets returns up to limit of orgID's PENDING assets
	// created at or before createdBefore, oldest first.
	ListExpiredAssets(ctx context.Context, orgID string, createdBefore time.Time, limit int) ([]*store.GtmAsset, error)
	// CreateAsset inserts row (its ID and object key set by the caller).
	CreateAsset(ctx context.Context, row *store.GtmAsset) error
	// MarkAssetReady makes a PENDING asset created after createdAfter READY
	// with its checked type, size and sha256; any other (not PENDING, or
	// expired) is ErrAssetNotPending.
	MarkAssetReady(ctx context.Context, orgID, id, contentType string, size int64, sha256 string, createdAfter time.Time) error
	// DeletePendingAsset removes orgID's asset id only while it is PENDING
	// and, when createdBefore is not zero, created at or before it; it
	// reports whether it did.
	DeletePendingAsset(ctx context.Context, orgID, id string, createdBefore time.Time) (bool, error)
	// DeleteAsset removes orgID's asset id; a missing one is not an error.
	DeleteAsset(ctx context.Context, orgID, id string) error
}

// Deps are the service's collaborators.
type Deps struct {
	// Store is the persistence (NewStore over the ORM).
	Store Store
	// Gate decides GTM entitlement (featuregate.Gate).
	Gate Gate
	// Clock is the time source; nil is the wall clock.
	Clock Clock
	// Analytics runs product-analytics templates; nil means analytics is not
	// configured, and ReadProductAnalytics is ErrUnavailable.
	Analytics Analytics
	// Email sends approved emails; nil means email is not configured, and
	// SendItemEmail is ErrUnavailable.
	Email Email
	// Connections computes channels' connection_state; nil is UNKNOWN.
	Connections Connections
	// Assets holds asset bytes; nil means assets are not configured, and
	// every byte-touching asset method is ErrAssetsUnavailable.
	Assets AssetStore
	// Attachments reads chat attachments; nil makes AttachFromAttachment
	// ErrAttachmentsUnavailable.
	Attachments Attachments
}
