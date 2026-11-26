// Copyright (c) 2026 Reliant Labs

// Package gtm is the GTM domain: an org's ideas, campaigns, items (posts and
// emails: the calendar), positioning and claims registry. Both the Connect
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
//     else. The item lifecycle is checkTransition's.
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
	// the actor's chat as its source chat.
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
	// built from the stored item, never from the caller. A send that may
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
	// UpdatePositioning replaces the org's positioning whole. Expected
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
}
