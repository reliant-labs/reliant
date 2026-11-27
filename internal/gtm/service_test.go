// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/store"
)

const (
	orgA = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	orgB = "org-b"
)

var (
	t0     = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	person = Actor{UserID: "u-person", OrgID: orgA, Kind: ActorPerson}
	agent  = Actor{UserID: "u-person", OrgID: orgA, Kind: ActorAgent, ChatID: "chat-1"}
)

// allowGate entitles the named orgs, for any user.
func allowGate(orgs ...string) *featuregate.MockGate {
	return &featuregate.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, userID, orgID string) (bool, error) {
		if f != featuregate.GTM || userID == "" {
			return false, nil
		}
		for _, o := range orgs {
			if o == orgID {
				return true, nil
			}
		}
		return false, nil
	}}
}

type fixture struct {
	svc       Service
	store     *fakeStore
	email     *MockEmail
	analytics *MockAnalytics
	blobs     *fakeBlobs
	files     *fakeAttachments
	conns     *MockConnections
	// now is the service's clock; advance moves it.
	now time.Time
}

// advance moves the service's clock on by d.
func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// pastInFlight moves the clock past the age a hold may still be in flight.
func (f *fixture) pastInFlight() { f.advance(2*EmailSendTimeout + time.Second) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		now:   t0,
		store: newFakeStore(),
		email: &MockEmail{SendFunc: sendsAs("gmail-1")},
		analytics: &MockAnalytics{QueryFunc: func(_ context.Context, _ string, q AnalyticsQuery) (AnalyticsResult, error) {
			return AnalyticsResult{Template: q.Template, From: q.From, To: q.To}, nil
		}},
		blobs: newFakeBlobs(),
		files: &fakeAttachments{files: map[string]fakeFile{}},
		conns: &MockConnections{StateFunc: func(context.Context, Actor, string, string) (ConnectionState, error) {
			return ConnectionConnected, nil
		}},
	}
	f.blobs.now = func() time.Time { return f.now }
	f.svc = New(Deps{
		Store:       f.store,
		Gate:        allowGate(orgA, orgB),
		Clock:       &MockClock{NowFunc: func() time.Time { return f.now }},
		Analytics:   f.analytics,
		Email:       f.email,
		Connections: f.conns,
		Assets:      f.blobs,
		Attachments: f.files,
	})
	return f
}

func (f *fixture) idea(t *testing.T, status IdeaStatus) Idea {
	t.Helper()
	i, err := f.svc.CreateIdea(context.Background(), person, CreateIdeaInput{Kind: IdeaKindContent, Title: "Write it"})
	require.NoError(t, err)
	if status != IdeaProposed {
		i, err = f.svc.SetIdeaStatus(context.Background(), person, SetIdeaStatusInput{ID: i.ID, ExpectedRevision: i.Revision, Status: status})
		require.NoError(t, err)
	}
	return i
}

func (f *fixture) emailItem(t *testing.T, status ItemStatus) Item {
	t.Helper()
	ctx := context.Background()
	it, err := f.svc.CreateItem(ctx, person, CreateItemInput{
		Format: FormatEmail, Title: "Launch mail", BodyMD: "Hello", EmailTo: []string{"a@example.com"}, EmailSubject: "News",
	})
	require.NoError(t, err)
	if status != ItemDraft {
		it, err = f.svc.UpdateItem(ctx, person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: &status})
		require.NoError(t, err)
	}
	return it
}

// Every method, called by an actor GTM refuses, is ErrNotFound and touches
// nothing behind the gate.
func TestEveryMethodIsNotFoundWhenTheGateRefuses(t *testing.T) {
	methods := map[string]func(Service, Actor) error{
		"ListIdeas": func(s Service, a Actor) error { _, err := s.ListIdeas(ctx(), a, ListIdeasInput{}); return err },
		"GetIdea":   func(s Service, a Actor) error { _, err := s.GetIdea(ctx(), a, GetIdeaInput{ID: "x"}); return err },
		"CreateIdea": func(s Service, a Actor) error {
			_, err := s.CreateIdea(ctx(), a, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t"})
			return err
		},
		"UpdateIdea": func(s Service, a Actor) error {
			_, err := s.UpdateIdea(ctx(), a, UpdateIdeaInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"SetIdeaStatus": func(s Service, a Actor) error {
			_, err := s.SetIdeaStatus(ctx(), a, SetIdeaStatusInput{ID: "x", ExpectedRevision: 1, Status: IdeaAccepted})
			return err
		},
		"ListCampaigns": func(s Service, a Actor) error { _, err := s.ListCampaigns(ctx(), a, ListCampaignsInput{}); return err },
		"GetCampaign": func(s Service, a Actor) error {
			_, err := s.GetCampaign(ctx(), a, GetCampaignInput{ID: "x"})
			return err
		},
		"CreateCampaign": func(s Service, a Actor) error {
			_, err := s.CreateCampaign(ctx(), a, CreateCampaignInput{Name: "n"})
			return err
		},
		"UpdateCampaign": func(s Service, a Actor) error {
			_, err := s.UpdateCampaign(ctx(), a, UpdateCampaignInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"ListItems": func(s Service, a Actor) error { _, err := s.ListItems(ctx(), a, ListItemsInput{}); return err },
		"GetItem":   func(s Service, a Actor) error { _, err := s.GetItem(ctx(), a, GetItemInput{ID: "x"}); return err },
		"CreateItem": func(s Service, a Actor) error {
			_, err := s.CreateItem(ctx(), a, CreateItemInput{Format: FormatLongPost, Title: "t"})
			return err
		},
		"UpdateItem": func(s Service, a Actor) error {
			_, err := s.UpdateItem(ctx(), a, UpdateItemInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"ResolveInDoubtSend": func(s Service, a Actor) error {
			_, err := s.ResolveInDoubtSend(ctx(), a, ResolveInDoubtSendInput{ItemID: "x", ApprovalHash: "h", GmailMessageID: ptr("m")})
			return err
		},
		"SendItemEmail": func(s Service, a Actor) error {
			_, err := s.SendItemEmail(ctx(), a, SendItemEmailInput{ItemID: "x", ExpectedRevision: 1, ApprovalHash: "h", Run: EmailRun{RunID: "r"}})
			return err
		},
		"GetPositioning": func(s Service, a Actor) error { _, err := s.GetPositioning(ctx(), a); return err },
		"UpdatePositioning": func(s Service, a Actor) error {
			_, err := s.UpdatePositioning(ctx(), a, UpdatePositioningInput{})
			return err
		},
		"ListClaims": func(s Service, a Actor) error { _, err := s.ListClaims(ctx(), a, ListClaimsInput{}); return err },
		"UpsertClaim": func(s Service, a Actor) error {
			_, err := s.UpsertClaim(ctx(), a, UpsertClaimInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"CheckClaims": func(s Service, a Actor) error {
			_, err := s.CheckClaims(ctx(), a, CheckClaimsInput{Text: "free"})
			return err
		},
		"ReadProductAnalytics": func(s Service, a Actor) error {
			_, err := s.ReadProductAnalytics(ctx(), a, AnalyticsQuery{Template: AnalyticsEventCounts})
			return err
		},
	}
	gateErr := errors.New("membership outage")
	refusals := map[string]struct {
		gate  Gate
		actor Actor
	}{
		"org not entitled":   {allowGate("another-org"), person},
		"gate error":         {&featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return true, gateErr }}, person},
		"no gate":            {nil, person},
		"actor without org":  {allowGate(orgA), Actor{UserID: "u", Kind: ActorPerson}},
		"actor without user": {allowGate(orgA), Actor{OrgID: orgA, Kind: ActorAgent}},
		"actor without kind": {allowGate(orgA), Actor{UserID: "u", OrgID: orgA}},
		"agent of other org": {allowGate(orgA), Actor{UserID: "u", OrgID: orgB, Kind: ActorAgent}},
	}
	for rname, r := range refusals {
		for mname, call := range methods {
			t.Run(rname+"/"+mname, func(t *testing.T) {
				st, em, an := &MockStore{}, &MockEmail{}, &MockAnalytics{}
				svc := New(Deps{Store: st, Gate: r.gate, Analytics: an, Email: em})
				err := call(svc, r.actor)
				require.ErrorIs(t, err, ErrNotFound)
				assert.Equal(t, "not found", err.Error(), "a refusal says nothing about GTM")
				for _, m := range []string{"InTx", "ListIdeas", "GetIdea", "ListCampaigns", "GetCampaign", "ListItems", "GetItem", "GetPositioning", "ListClaims", "GetClaim"} {
					assert.Zero(t, st.CallCount(m), m)
				}
				assert.Zero(t, em.CallCount("Send"))
				assert.Zero(t, an.CallCount("Query"))
			})
		}
		t.Run(rname+"/GetAccess", func(t *testing.T) {
			got, err := New(Deps{Store: &MockStore{}, Gate: r.gate}).GetAccess(ctx(), r.actor)
			require.NoError(t, err)
			assert.False(t, got.Enabled)
		})
	}
	t.Run("a gate error is logged, never attached", func(t *testing.T) {
		g := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return false, gateErr }}
		svc := New(Deps{Store: &MockStore{}, Gate: g})
		var logged bytes.Buffer
		svc.(*service).log = slog.New(slog.NewTextHandler(&logged, nil))
		_, err := svc.ListIdeas(ctx(), person, ListIdeasInput{})
		require.ErrorIs(t, err, ErrNotFound)
		assert.NotErrorIs(t, err, gateErr)
		assert.Nil(t, svcerr.Cause(err))
		assert.Contains(t, logged.String(), "membership outage")
	})
	t.Run("entitled", func(t *testing.T) {
		got, err := newFixture(t).svc.GetAccess(ctx(), person)
		require.NoError(t, err)
		assert.True(t, got.Enabled)
	})
}

func ctx() context.Context { return context.Background() }

func TestOrgsAreIsolated(t *testing.T) {
	f := newFixture(t)
	i := f.idea(t, IdeaProposed)
	other := Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}

	_, err := f.svc.GetIdea(ctx(), other, GetIdeaInput{ID: i.ID})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.UpdateIdea(ctx(), other, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, Title: ptr("mine")})
	require.ErrorIs(t, err, ErrNotFound)
	list, err := f.svc.ListIdeas(ctx(), other, ListIdeasInput{})
	require.NoError(t, err)
	assert.Empty(t, list.Ideas)
}

func TestCrossOrgLinksAreRefused(t *testing.T) {
	f := newFixture(t)
	other := Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}
	c, err := f.svc.CreateCampaign(ctx(), other, CreateCampaignInput{Name: "B's", StrategyID: f.strategy(t, other)})
	require.NoError(t, err)
	bIdea, err := f.svc.CreateIdea(ctx(), other, CreateIdeaInput{Kind: IdeaKindGTM, Title: "B's idea"})
	require.NoError(t, err)

	var link *LinkError
	_, err = f.svc.CreateIdea(ctx(), person, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t", CampaignID: c.ID})
	require.ErrorAs(t, err, &link)
	assert.Equal(t, "campaign_id", link.Field)
	assert.ErrorIs(t, err, svcerr.ErrNotFound, "a link to another org's row is NotFound")

	_, err = f.svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatLongPost, Title: "t", IdeaID: bIdea.ID})
	require.ErrorAs(t, err, &link)
	assert.Equal(t, "idea_id", link.Field)

	it, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatLongPost, Title: "t"})
	require.NoError(t, err)
	_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, CampaignID: &c.ID})
	require.ErrorAs(t, err, &link)

	t.Run("the foreign key's refusal is the same LinkError", func(t *testing.T) {
		st := &MockStore{
			InTxFunc:        func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
			GetCampaignFunc: func(context.Context, string, string) (*store.GtmCampaign, error) { return &store.GtmCampaign{}, nil },
			CreateItemFunc:  func(context.Context, *store.GtmItem) error { return &LinkError{Field: "campaign_id"} },
		}
		svc := New(Deps{Store: st, Gate: allowGate(orgA)})
		_, err := svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatLongPost, Title: "t", CampaignID: "c"})
		require.ErrorIs(t, err, ErrLinkNotFound)
	})
}

func TestRevisionConflicts(t *testing.T) {
	f := newFixture(t)
	i := f.idea(t, IdeaProposed)
	i2, err := f.svc.UpdateIdea(ctx(), person, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, Title: ptr("v2")})
	require.NoError(t, err)
	assert.Equal(t, i.Revision+1, i2.Revision)
	assert.Equal(t, "v2", i2.Title)

	_, err = f.svc.UpdateIdea(ctx(), person, UpdateIdeaInput{ID: i.ID, ExpectedRevision: i.Revision, Title: ptr("stale")})
	var rc *RevisionConflictError
	require.ErrorAs(t, err, &rc)
	assert.Equal(t, RevisionConflictError{ID: i.ID, Expected: i.Revision, Current: i2.Revision}, *rc)
	assert.ErrorIs(t, err, ErrRevisionConflict)
	assert.ErrorIs(t, err, svcerr.ErrAborted)

	t.Run("a row that moves between the read and the write reports where it is now", func(t *testing.T) {
		reads := 0
		st := &MockStore{
			InTxFunc: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
			GetIdeaFunc: func(_ context.Context, org, id string) (*store.GtmIdea, error) {
				reads++
				rev := int32(3)
				if reads > 1 {
					rev = 4
				}
				return &store.GtmIdea{ID: id, OrgID: org, Status: string(IdeaProposed), Revision: rev}, nil
			},
			UpdateIdeaFunc: func(context.Context, *store.GtmIdea, int32) error { return ErrRevisionConflict },
		}
		_, err := New(Deps{Store: st, Gate: allowGate(orgA)}).UpdateIdea(ctx(), person, UpdateIdeaInput{ID: "i", ExpectedRevision: 3, Title: ptr("x")})
		var rc *RevisionConflictError
		require.ErrorAs(t, err, &rc)
		assert.Equal(t, int32(4), rc.Current)
	})

	t.Run("positioning", func(t *testing.T) {
		p, err := f.svc.GetPositioning(ctx(), person)
		require.NoError(t, err)
		assert.Zero(t, p.Revision)
		p, err = f.svc.UpdatePositioning(ctx(), agent, UpdatePositioningInput{PositioningMD: "We help", Audiences: []string{"devs"}})
		require.NoError(t, err)
		assert.Equal(t, int32(1), p.Revision)
		assert.Equal(t, agent.UserID, p.UpdatedBy)
		_, err = f.svc.UpdatePositioning(ctx(), person, UpdatePositioningInput{PositioningMD: "again"})
		require.ErrorAs(t, err, &rc)
		assert.Equal(t, int32(1), rc.Current)
		p, err = f.svc.UpdatePositioning(ctx(), person, UpdatePositioningInput{ExpectedRevision: 1, PositioningMD: "again"})
		require.NoError(t, err)
		assert.Equal(t, int32(2), p.Revision)
	})
}

func TestAgentLimits(t *testing.T) {
	t.Run("an agent's idea is PROPOSED, sourced AGENT, with its chat", func(t *testing.T) {
		f := newFixture(t)
		i, err := f.svc.CreateIdea(ctx(), agent, CreateIdeaInput{Kind: IdeaKindProduct, Title: "Faster onboarding"})
		require.NoError(t, err)
		assert.Equal(t, IdeaProposed, i.Status)
		assert.Equal(t, IdeaSourceAgent, i.Source)
		assert.Equal(t, "chat-1", i.SourceChatID)
		assert.Equal(t, agent.UserID, i.CreatedBy)

		h, err := f.svc.CreateIdea(ctx(), person, CreateIdeaInput{Kind: IdeaKindProduct, Title: "Mine"})
		require.NoError(t, err)
		assert.Equal(t, IdeaSourceHuman, h.Source)
	})
	t.Run("an agent neither accepts, rejects nor moves an idea", func(t *testing.T) {
		f := newFixture(t)
		i := f.idea(t, IdeaProposed)
		for _, st := range []IdeaStatus{IdeaAccepted, IdeaRejected, IdeaDone} {
			_, err := f.svc.SetIdeaStatus(ctx(), agent, SetIdeaStatusInput{ID: i.ID, ExpectedRevision: i.Revision, Status: st})
			require.ErrorIs(t, err, ErrPersonOnly, st)
			assert.ErrorIs(t, err, svcerr.ErrPermissionDenied)
		}
		got, err := f.svc.SetIdeaStatus(ctx(), person, SetIdeaStatusInput{ID: i.ID, ExpectedRevision: i.Revision, Status: IdeaAccepted})
		require.NoError(t, err)
		assert.Equal(t, IdeaAccepted, got.Status)
	})
	t.Run("an agent edits proposed ideas only", func(t *testing.T) {
		f := newFixture(t)
		p := f.idea(t, IdeaProposed)
		_, err := f.svc.UpdateIdea(ctx(), agent, UpdateIdeaInput{ID: p.ID, ExpectedRevision: p.Revision, BodyMD: ptr("more")})
		require.NoError(t, err)
		a := f.idea(t, IdeaAccepted)
		_, err = f.svc.UpdateIdea(ctx(), agent, UpdateIdeaInput{ID: a.ID, ExpectedRevision: a.Revision, BodyMD: ptr("more")})
		require.ErrorIs(t, err, ErrPersonOnly)
	})
	t.Run("only a person approves an item", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemDraft)
		_, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.ErrorIs(t, err, ErrPersonOnly)
		_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemPaused)})
		require.ErrorIs(t, err, ErrPersonOnly)
		for _, st := range []ItemStatus{ItemScheduled, ItemPublished} {
			_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(st)})
			require.ErrorIs(t, err, ErrInvalidTransition, "an email draft can never get there: %s", st)
		}
		got, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemReview), BodyMD: ptr("v2")})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status)
	})
	t.Run("an agent touches DRAFT and REVIEW items only", func(t *testing.T) {
		f := newFixture(t)
		when := t0.Add(48 * time.Hour)
		for _, st := range []ItemStatus{ItemApproved, ItemScheduled, ItemPublished, ItemPaused} {
			it := f.itemAt(t, FormatLongPost, st)
			for name, in := range map[string]UpdateItemInput{
				"content":    {BodyMD: ptr("sneaky")},
				"date":       {ScheduledAt: &when},
				"clear date": {ClearScheduledAt: true},
				"link":       {CampaignID: ptr("")},
				"status":     {Status: ptr(ItemDraft)},
			} {
				in.ID, in.ExpectedRevision = it.ID, it.Revision
				_, err := f.svc.UpdateItem(ctx(), agent, in)
				require.ErrorIs(t, err, ErrPersonOnly, "%s: %s", st, name)
			}
		}
		it := f.itemAt(t, FormatLongPost, ItemReview)
		got, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, ScheduledAt: &when, BodyMD: ptr("v2")})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status, "a date never publishes")
		assert.True(t, got.ScheduledAt.Equal(when))
		got, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, ClearScheduledAt: true, Status: ptr(ItemDraft)})
		require.NoError(t, err)
		assert.Nil(t, got.ScheduledAt)
		assert.Equal(t, ItemDraft, got.Status)
	})
	t.Run("an agent's item starts DRAFT", func(t *testing.T) {
		f := newFixture(t)
		it, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "t"})
		require.NoError(t, err)
		assert.Equal(t, ItemDraft, it.Status)
	})
	t.Run("only a person verifies a claim or changes a verified one's evidence", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{
			Text: ptr("Hosting costs $5 a month."), Category: ptr(ClaimPricing), Status: ptr(ClaimVerified),
			SourceURL: ptr("https://example.com/pricing"), SourceKind: ptr(ClaimSourcePricingCatalog),
		})
		require.ErrorIs(t, err, ErrPersonOnly)

		res, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{
			Text: ptr("Hosting costs $5 a month."), Category: ptr(ClaimPricing), Status: ptr(ClaimVerified),
			SourceURL: ptr("https://example.com/pricing"), SourceKind: ptr(ClaimSourcePricingCatalog),
		})
		require.NoError(t, err)
		c := res.Claim
		_, err = f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{ID: c.ID, ExpectedRevision: c.Revision, SourceURL: ptr("https://example.com/other")})
		require.ErrorIs(t, err, ErrPersonOnly)
		for name, in := range map[string]UpsertClaimInput{
			"retire":      {Status: ptr(ClaimRetired)},
			"unverify":    {Status: ptr(ClaimUnverified)},
			"source kind": {SourceKind: ptr(ClaimSourceDocs)},
			"recheck":     {ExpiresOrRecheckAt: &t0},
			"notes":       {Notes: ptr("hm")},
		} {
			in.ID, in.ExpectedRevision = c.ID, c.Revision
			_, err = f.svc.UpsertClaim(ctx(), agent, in)
			require.ErrorIs(t, err, ErrPersonOnly, "verified: "+name)
		}

		retired, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{ID: c.ID, ExpectedRevision: c.Revision, Status: ptr(ClaimRetired)})
		require.NoError(t, err)
		_, err = f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{ID: c.ID, ExpectedRevision: retired.Claim.Revision, Notes: ptr("revive?")})
		require.ErrorIs(t, err, ErrPersonOnly, "a retired claim is a person's")
		_, err = f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{ID: c.ID, ExpectedRevision: retired.Claim.Revision, Status: ptr(ClaimUnverified)})
		require.ErrorIs(t, err, ErrPersonOnly)
	})
	t.Run("an agent creates and edits UNVERIFIED claims only", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{Text: ptr("Old fact"), Category: ptr(ClaimLimit), Status: ptr(ClaimRetired)})
		require.ErrorIs(t, err, ErrPersonOnly)
		res, err := f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{Text: ptr("Up to 10 seats"), Category: ptr(ClaimLimit)})
		require.NoError(t, err)
		assert.Equal(t, ClaimUnverified, res.Claim.Status)
		got, err := f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{
			ID: res.Claim.ID, ExpectedRevision: res.Claim.Revision,
			SourceURL: ptr("https://example.com/limits"), SourceKind: ptr(ClaimSourceDocs), Notes: ptr("from the docs"),
		})
		require.NoError(t, err, "an agent may propose the evidence; a person verifies")
		assert.Equal(t, ClaimUnverified, got.Claim.Status)
		_, err = f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{ID: res.Claim.ID, ExpectedRevision: got.Claim.Revision, Status: ptr(ClaimRetired)})
		require.ErrorIs(t, err, ErrPersonOnly)
	})
	t.Run("a duplicate claim text is AlreadyExists", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{Text: ptr("Up to 10 seats"), Category: ptr(ClaimLimit)})
		require.NoError(t, err)
		_, err = f.svc.UpsertClaim(ctx(), agent, UpsertClaimInput{Text: ptr("Up to 10 seats"), Category: ptr(ClaimLimit)})
		require.ErrorIs(t, err, ErrClaimExists)
		assert.ErrorIs(t, err, svcerr.ErrAlreadyExists)
	})
}

func TestClaims(t *testing.T) {
	t.Run("a verified claim needs a source and gets its verifier", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{Text: ptr("Builds finish in 36 hours"), Category: ptr(ClaimTiming), Status: ptr(ClaimVerified)})
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "source_url", ve.Field)

		res, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{Text: ptr("Builds finish in 36 hours"), Category: ptr(ClaimTiming)})
		require.NoError(t, err)
		assert.True(t, res.Created)
		assert.Equal(t, ClaimUnverified, res.Claim.Status)

		_, err = f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{ID: res.Claim.ID, ExpectedRevision: 1, Status: ptr(ClaimVerified), SourceURL: ptr("https://example.com/docs")})
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "source_kind", ve.Field)

		got, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{
			ID: res.Claim.ID, ExpectedRevision: 1, Status: ptr(ClaimVerified),
			SourceURL: ptr("https://example.com/docs"), SourceKind: ptr(ClaimSourceDocs),
		})
		require.NoError(t, err)
		assert.False(t, got.Created)
		assert.Equal(t, person.UserID, got.Claim.VerifiedBy)
		require.NotNil(t, got.Claim.VerifiedAt)
		assert.True(t, got.Claim.VerifiedAt.Equal(t0))
	})
	t.Run("a verified comparison needs a primary third-party source", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{
			Text: ptr("Faster than Acme"), Category: ptr(ClaimComparison), Status: ptr(ClaimVerified),
			SourceURL: ptr("https://example.com"), SourceKind: ptr(ClaimSourceDocs),
		})
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "source_kind", ve.Field)
	})
	t.Run("text and category never change", func(t *testing.T) {
		f := newFixture(t)
		res, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{Text: ptr("Free for teams"), Category: ptr(ClaimPricing)})
		require.NoError(t, err)
		_, err = f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{ID: res.Claim.ID, ExpectedRevision: 1, Text: ptr("Free for everyone")})
		require.ErrorIs(t, err, ErrInvalidArgument)
		_, err = f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{ID: res.Claim.ID, ExpectedRevision: 1, Category: ptr(ClaimLicense)})
		require.ErrorIs(t, err, ErrInvalidArgument)
		_, err = f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{ID: res.Claim.ID, ExpectedRevision: 1, Text: ptr("Free for teams"), Notes: ptr("ok")})
		require.NoError(t, err, "the same text is no change")
	})
	t.Run("CheckClaims matches against the org's registry", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.UpsertClaim(ctx(), person, UpsertClaimInput{
			Text: ptr("Hosting costs $5 a month."), Category: ptr(ClaimPricing), Status: ptr(ClaimVerified),
			SourceURL: ptr("https://example.com/pricing"), SourceKind: ptr(ClaimSourcePricingCatalog),
		})
		require.NoError(t, err)

		ok, err := f.svc.CheckClaims(ctx(), agent, CheckClaimsInput{Text: "Hosting costs $5 a month."})
		require.NoError(t, err)
		require.NotEmpty(t, ok.Findings)
		assert.False(t, ok.Blocking)
		assert.Equal(t, FindingMatchedVerified, ok.Findings[0].Kind)
		assert.NotEmpty(t, ok.Findings[0].ClaimID)

		bad, err := f.svc.CheckClaims(ctx(), agent, CheckClaimsInput{Text: "Hosting costs $9 a month."})
		require.NoError(t, err)
		assert.True(t, bad.Blocking)

		other, err := f.svc.CheckClaims(ctx(), Actor{UserID: "u", OrgID: orgB, Kind: ActorAgent}, CheckClaimsInput{Text: "Hosting costs $5 a month."})
		require.NoError(t, err)
		assert.True(t, other.Blocking, "another org's registry vouches for nothing")
	})
}

func TestReadProductAnalytics(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventTrend, Events: []string{"signup", "chat.created"}})
	require.NoError(t, err)
	require.Equal(t, 1, f.analytics.CallCount("Query"))
	call := f.analytics.Calls("Query")[0]
	assert.Equal(t, orgA, call.Args[1], "the actor's org, never one the caller names")
	q := call.Args[2].(AnalyticsQuery)
	assert.Equal(t, t0, q.To)
	assert.Equal(t, t0.Add(-DefaultAnalyticsWindow), q.From)
	assert.Equal(t, DefaultAnalyticsRows, q.Limit)
	assert.Equal(t, q.From, res.From)

	for name, bad := range map[string]AnalyticsQuery{
		"unknown template":       {Template: "sql"},
		"injection in a name":    {Template: AnalyticsEventTrend, Events: []string{"x' OR 1=1 --"}},
		"breakdown without prop": {Template: AnalyticsPropertyBreakdown, Events: []string{"signup"}},
		"property elsewhere":     {Template: AnalyticsEventCounts, Property: "plan"},
		"one-step funnel":        {Template: AnalyticsFunnel, Events: []string{"a"}},
		"sql window over 30d":    {Template: AnalyticsEventCounts, From: t0.Add(-31 * 24 * time.Hour), To: t0},
		"trend window over 30d":  {Template: AnalyticsEventTrend, Events: []string{"a"}, From: t0.Add(-31 * 24 * time.Hour), To: t0},
		"breakdown over 30d":     {Template: AnalyticsPropertyBreakdown, Events: []string{"a"}, Property: "p", From: t0.Add(-31 * 24 * time.Hour), To: t0},
		"funnel window over 90d": {Template: AnalyticsFunnel, Events: []string{"a", "b"}, From: t0.Add(-91 * 24 * time.Hour), To: t0},
		"too many rows":          {Template: AnalyticsEventCounts, Limit: MaxAnalyticsRows + 1},
		"too many trend events":  {Template: AnalyticsEventTrend, Events: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.ReadProductAnalytics(ctx(), agent, bad)
			require.ErrorIs(t, err, ErrInvalidArgument)
		})
	}
	assert.Equal(t, 1, f.analytics.CallCount("Query"), "an invalid query never reaches Analytics")

	_, err = New(Deps{Store: newFakeStore(), Gate: allowGate(orgA)}).ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventCounts})
	require.ErrorIs(t, err, ErrAnalyticsUnavailable)

	t.Run("sql templates read up to 30 days, a funnel up to 90", func(t *testing.T) {
		f := newFixture(t)
		for _, q := range []AnalyticsQuery{
			{Template: AnalyticsEventCounts, From: t0.Add(-MaxSQLAnalyticsWindow), To: t0},
			{Template: AnalyticsEventTrend, Events: []string{"a"}, From: t0.Add(-30 * 24 * time.Hour), To: t0},
			{Template: AnalyticsPropertyBreakdown, Events: []string{"a"}, Property: "p", From: t0.Add(-30 * 24 * time.Hour), To: t0},
			{Template: AnalyticsFunnel, Events: []string{"a", "b"}, From: t0.Add(-90 * 24 * time.Hour), To: t0},
			{Template: AnalyticsFunnel, Events: []string{"a", "b"}, From: t0.Add(-45 * 24 * time.Hour), To: t0},
		} {
			_, err := f.svc.ReadProductAnalytics(ctx(), agent, q)
			require.NoError(t, err, q.Template)
		}
		assert.Equal(t, 30*24*time.Hour, MaxSQLAnalyticsWindow)
	})
	t.Run("the client's error text never reaches the caller", func(t *testing.T) {
		f := newFixture(t)
		f.analytics.QueryFunc = func(context.Context, string, AnalyticsQuery) (AnalyticsResult, error) {
			return AnalyticsResult{}, errors.New(`control-plane 10.1.2.3: SELECT secret FROM product_events failed`)
		}
		_, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventCounts})
		require.ErrorIs(t, err, ErrAnalyticsFailed)
		assert.Equal(t, "the product analytics query failed", err.Error())
		assert.Nil(t, svcerr.Cause(err))
	})
	t.Run("rows are capped at the limit in the domain", func(t *testing.T) {
		f := newFixture(t)
		f.analytics.QueryFunc = func(_ context.Context, _ string, q AnalyticsQuery) (AnalyticsResult, error) {
			rows := make([]AnalyticsRow, 30)
			return AnalyticsResult{Template: "other", Rows: rows}, nil
		}
		res, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventCounts, Limit: 10})
		require.NoError(t, err)
		assert.Len(t, res.Rows, 10)
		assert.True(t, res.Truncated)
		assert.Equal(t, AnalyticsEventCounts, res.Template)
		res, err = f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventCounts, Limit: 50})
		require.NoError(t, err)
		assert.Len(t, res.Rows, 30)
		assert.False(t, res.Truncated)
	})
}

func TestListsPageAndFilter(t *testing.T) {
	f := newFixture(t)
	for range 5 {
		f.idea(t, IdeaProposed)
	}
	f.idea(t, IdeaAccepted)
	var seen []string
	token := ""
	for {
		res, err := f.svc.ListIdeas(ctx(), person, ListIdeasInput{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		for _, i := range res.Ideas {
			seen = append(seen, i.ID)
		}
		if res.NextPageToken == "" {
			break
		}
		token = res.NextPageToken
	}
	assert.Len(t, seen, 6)

	acc, err := f.svc.ListIdeas(ctx(), person, ListIdeasInput{Status: IdeaAccepted})
	require.NoError(t, err)
	assert.Len(t, acc.Ideas, 1)

	_, err = f.svc.ListIdeas(ctx(), person, ListIdeasInput{PageToken: "garbage"})
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = f.svc.ListIdeas(ctx(), person, ListIdeasInput{PageSize: MaxIdeaPageSize + 1})
	require.ErrorIs(t, err, ErrInvalidArgument)

	t.Run("the calendar is a scheduled range", func(t *testing.T) {
		in, out := t0.Add(24*time.Hour), t0.Add(30*24*time.Hour)
		_, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "in", ScheduledAt: &in})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "out", ScheduledAt: &out})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "undated"})
		require.NoError(t, err)
		from, to := t0, t0.Add(7*24*time.Hour)
		res, err := f.svc.ListItems(ctx(), person, ListItemsInput{ScheduledFrom: &from, ScheduledTo: &to})
		require.NoError(t, err)
		require.Len(t, res.Items, 1)
		assert.Equal(t, "in", res.Items[0].Title)
	})
}

func TestCampaignDetail(t *testing.T) {
	f := newFixture(t)
	c, err := f.svc.CreateCampaign(ctx(), agent, CreateCampaignInput{Name: "Q1 launch", Goal: "100 signups", StrategyID: f.strategy(t, agent)})
	require.NoError(t, err)
	assert.Equal(t, CampaignDraft, c.Status)
	_, err = f.svc.CreateIdea(ctx(), agent, CreateIdeaInput{Kind: IdeaKindGTM, Title: "Launch post", CampaignID: c.ID})
	require.NoError(t, err)
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatLongPost, Title: "Post", CampaignID: c.ID})
	require.NoError(t, err)
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatLongPost, Title: "Unlinked"})
	require.NoError(t, err)

	d, err := f.svc.GetCampaign(ctx(), person, GetCampaignInput{ID: c.ID})
	require.NoError(t, err)
	assert.Equal(t, c.ID, d.Campaign.ID)
	assert.Len(t, d.Ideas, 1)
	assert.Len(t, d.Items, 1)

	start, end := t0, t0.Add(-time.Hour)
	_, err = f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, PeriodStart: &start, PeriodEnd: &end})
	require.ErrorIs(t, err, ErrInvalidArgument)
	u, err := f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, Status: ptr(CampaignActive)})
	require.NoError(t, err)
	assert.Equal(t, CampaignActive, u.Status)
}

func TestItemValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatLongPost, Title: "t", EmailTo: []string{"a@example.com"}})
	require.ErrorIs(t, err, ErrInvalidArgument, "only an email has recipients")
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatEmail, Title: "t", EmailTo: []string{"Bob <b@example.com>"}})
	require.ErrorIs(t, err, ErrInvalidArgument, "bare addresses only")
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatEmail, Title: "  "})
	require.ErrorIs(t, err, ErrInvalidArgument)

	// Invisible and bidi format characters make an address or a subject read
	// as other than what is sent; net/mail alone accepts them.
	for name, addr := range map[string]string{
		"RLO":       "evil\u202e@example.com",
		"ZWSP":      "a\u200b@example.com",
		"LRI":       "\u2066a@example.com",
		"BOM":       "\ufeffa@example.com",
		"soft hyph": "pay\u00adpal@example.com",
		"IDN":       "bob@exämple.com",
		"non-ASCII": "bøb@example.com",
	} {
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatEmail, Title: "t", EmailTo: []string{addr}})
		require.ErrorIs(t, err, ErrInvalidArgument, name)
	}
	for name, subject := range map[string]string{
		"RLO":  "Invoice \u202efdp.exe",
		"ZWSP": "Hel\u200blo",
		"LRI":  "\u2066News\u2069",
		"BOM":  "\ufeffNews",
		"CRLF": "News\r\nBcc: x@example.com",
	} {
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatEmail, Title: "t", EmailTo: []string{"a@example.com"}, EmailSubject: subject})
		require.ErrorIs(t, err, ErrInvalidArgument, name)
	}
	ok, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatEmail, Title: "t", EmailTo: []string{"a.b+c@example.com"}, EmailSubject: "Café ☕ news, 日本語"})
	require.NoError(t, err, "a non-ASCII subject is fine; only format and control characters are refused")
	_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: ok.ID, ExpectedRevision: ok.Revision, EmailSubject: ptr("x\u202ey")})
	require.ErrorIs(t, err, ErrInvalidArgument, "on update too")
	_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: ok.ID, ExpectedRevision: ok.Revision, EmailTo: &[]string{"a\u200b@example.com"}})
	require.ErrorIs(t, err, ErrInvalidArgument, "on update too")

	it := f.itemAt(t, FormatLongPost, ItemApproved)
	_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemScheduled)})
	require.ErrorIs(t, err, ErrInvalidArgument, "a scheduled item needs a date")
}

// strategy makes a DRAFT strategy for a's org and returns its id.
func (f *fixture) strategy(t *testing.T, a Actor) string {
	t.Helper()
	st, err := f.svc.CreateStrategy(ctx(), a, CreateStrategyInput{Name: "Grow"})
	require.NoError(t, err)
	return st.ID
}

// channel returns the org's (seeded) channel key.
func (f *fixture) channel(t *testing.T, key string) Channel {
	t.Helper()
	res, err := f.svc.ListChannels(ctx(), person, ListChannelsInput{})
	require.NoError(t, err)
	for _, ch := range res.Channels {
		if ch.Key == key {
			return ch
		}
	}
	t.Fatalf("no channel %q", key)
	return Channel{}
}

// itemAt makes an item of kind and walks it, as a person, to status.
func (f *fixture) itemAt(t *testing.T, format ItemFormat, status ItemStatus) Item {
	t.Helper()
	in := CreateItemInput{Format: format, Title: "Item", BodyMD: "Body"}
	if format.EmailType() {
		in.EmailTo, in.EmailSubject = []string{"a@example.com"}, "Subject"
	}
	if status == ItemScheduled {
		when := t0.Add(24 * time.Hour)
		in.ScheduledAt = &when
	}
	it, err := f.svc.CreateItem(ctx(), person, in)
	require.NoError(t, err)
	path := map[ItemStatus][]ItemStatus{
		ItemDraft:     nil,
		ItemReview:    {ItemReview},
		ItemApproved:  {ItemApproved},
		ItemScheduled: {ItemApproved, ItemScheduled},
		ItemPublished: {ItemApproved, ItemPublished},
		ItemPaused:    {ItemPaused},
	}[status]
	for _, st := range path {
		it, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(st)})
		require.NoError(t, err)
	}
	require.Equal(t, status, it.Status)
	return it
}

func TestItemLifecycle(t *testing.T) {
	all := []ItemStatus{ItemDraft, ItemReview, ItemApproved, ItemScheduled, ItemPublished, ItemPaused}
	allowed := func(format ItemFormat, from, to ItemStatus) bool {
		email := format.EmailType()
		switch {
		case (from == ItemDraft || from == ItemReview) && (to == ItemDraft || to == ItemReview || to == ItemApproved):
			return true
		case from == ItemApproved && to == ItemScheduled:
			return !email
		case (from == ItemApproved || from == ItemScheduled) && to == ItemPublished:
			return !email
		case to == ItemPaused:
			return true
		case from == ItemPaused && to == ItemDraft:
			return true
		}
		return false
	}
	for _, kind := range []ItemFormat{FormatLongPost, FormatEmail, FormatColdOutreach} {
		for _, from := range all {
			if kind.EmailType() && (from == ItemScheduled || from == ItemPublished) {
				continue // an email never gets there
			}
			for _, to := range all {
				if to == from {
					continue
				}
				t.Run(statusWord(from)+"->"+statusWord(to)+"/"+string(kind), func(t *testing.T) {
					f := newFixture(t)
					it := f.itemAt(t, kind, from)
					in := UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(to)}
					if to == ItemScheduled && it.ScheduledAt == nil {
						when := t0.Add(time.Hour)
						in.ScheduledAt = &when
					}
					got, err := f.svc.UpdateItem(ctx(), person, in)
					if allowed(kind, from, to) {
						require.NoError(t, err)
						assert.Equal(t, to, got.Status)
						return
					}
					var te *TransitionError
					require.ErrorAs(t, err, &te)
					assert.ErrorIs(t, err, svcerr.ErrFailedPrecondition)
				})
			}
		}
	}

	t.Run("a content edit drops an approved or scheduled item to REVIEW", func(t *testing.T) {
		f := newFixture(t)
		for _, st := range []ItemStatus{ItemApproved, ItemScheduled} {
			it := f.itemAt(t, FormatLongPost, st)
			got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Title: ptr("New title")})
			require.NoError(t, err)
			assert.Equal(t, ItemReview, got.Status, st)

			it = f.itemAt(t, FormatLongPost, st)
			got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, BodyMD: ptr("New"), Status: ptr(ItemApproved)})
			require.NoError(t, err, "edit and re-approve in one call")
			assert.Equal(t, ItemApproved, got.Status)
		}
		it := f.itemAt(t, FormatLongPost, ItemApproved)
		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Title: ptr(it.Title), ChannelID: ptr(it.ChannelID)})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status, "the same content is no change")
		email := f.itemAt(t, FormatEmail, ItemApproved)
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: email.ID, ExpectedRevision: email.Revision, EmailTo: &[]string{"b@example.com"}})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status, "recipients are content")
	})
	t.Run("a person dates an approved email and it stays APPROVED", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatEmail, ItemApproved)
		when := t0.Add(72 * time.Hour)
		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, ScheduledAt: &when})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status)
		assert.True(t, got.ScheduledAt.Equal(when))
	})
	t.Run("an email cannot become SCHEDULED or PUBLISHED by changing kind", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatLongPost, ItemPublished)
		_, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{
			ID: it.ID, ExpectedRevision: it.Revision, Format: ptr(FormatEmail),
		})
		require.ErrorIs(t, err, ErrInvalidTransition)
	})
	t.Run("an agent asking for a move no one may make is FailedPrecondition", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatLongPost, ItemDraft)
		for _, to := range []ItemStatus{ItemScheduled, ItemPublished} {
			_, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(to)})
			require.ErrorIs(t, err, ErrInvalidTransition, to)
			assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		}
		for _, to := range []ItemStatus{ItemApproved, ItemPaused} {
			_, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(to)})
			require.ErrorIs(t, err, ErrPersonOnly, to)
			assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
		}
	})
	t.Run("a person does everything an agent may on DRAFT and REVIEW items", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatShortPost, ItemDraft)
		when := t0.Add(5 * time.Hour)
		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemReview), BodyMD: ptr("v2"), ScheduledAt: &when})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status)
		assert.Equal(t, "v2", got.BodyMD)
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, Status: ptr(ItemDraft), Title: ptr("v3"), ClearScheduledAt: true})
		require.NoError(t, err)
		assert.Equal(t, ItemDraft, got.Status)
		assert.Nil(t, got.ScheduledAt)
	})
	t.Run("only a date never drops an item to REVIEW", func(t *testing.T) {
		f := newFixture(t)
		for _, it := range []Item{f.itemAt(t, FormatLongPost, ItemApproved), f.itemAt(t, FormatLongPost, ItemScheduled), f.itemAt(t, FormatEmail, ItemApproved)} {
			when := t0.Add(9 * time.Hour)
			got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, ScheduledAt: &when})
			require.NoError(t, err)
			assert.Equal(t, it.Status, got.Status)
			assert.True(t, got.ScheduledAt.Equal(when))
		}
		for name, in := range map[string]UpdateItemInput{
			"title":   {Title: ptr("new")},
			"body":    {BodyMD: ptr("new")},
			"format":  {Format: ptr(FormatOther)},
			"channel": {ChannelID: ptr(f.channel(t, "blog").ID)},
		} {
			it := f.itemAt(t, FormatLongPost, ItemScheduled)
			in.ID, in.ExpectedRevision = it.ID, it.Revision
			got, err := f.svc.UpdateItem(ctx(), person, in)
			require.NoError(t, err)
			assert.Equal(t, ItemReview, got.Status, name)
		}
		for name, in := range map[string]UpdateItemInput{
			"email_to":      {EmailTo: &[]string{"z@example.com"}},
			"email_subject": {EmailSubject: ptr("new")},
		} {
			it := f.itemAt(t, FormatEmail, ItemApproved)
			in.ID, in.ExpectedRevision = it.ID, it.Revision
			got, err := f.svc.UpdateItem(ctx(), person, in)
			require.NoError(t, err)
			assert.Equal(t, ItemReview, got.Status, name)
		}
	})
	t.Run("clearing a scheduled item's date is a refused transition, not a CHECK", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatLongPost, ItemScheduled)
		_, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, ClearScheduledAt: true})
		var te *TransitionError
		require.ErrorAs(t, err, &te)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.ErrorAs(t, err, &te, "there is no SCHEDULED -> APPROVED")

		// The way off the calendar: PAUSED, DRAFT, approve again.
		got := it
		for _, st := range []ItemStatus{ItemPaused, ItemDraft} {
			got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, Status: ptr(st)})
			require.NoError(t, err)
		}
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, ClearScheduledAt: true, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status)
		assert.Nil(t, got.ScheduledAt)
	})
	t.Run("an item is created DRAFT or REVIEW", func(t *testing.T) {
		f := newFixture(t)
		for _, a := range []Actor{person, agent} {
			it, err := f.svc.CreateItem(ctx(), a, CreateItemInput{Format: FormatLongPost, Title: "t", Status: ItemReview})
			require.NoError(t, err)
			assert.Equal(t, ItemReview, it.Status)
			for _, st := range []ItemStatus{ItemApproved, ItemScheduled, ItemPublished, ItemPaused, ItemSent} {
				_, err := f.svc.CreateItem(ctx(), a, CreateItemInput{Format: FormatLongPost, Title: "t", Status: st})
				require.ErrorIs(t, err, ErrInvalidArgument, st)
			}
		}
	})
}

func TestSentIsWrittenOnlyByTheEmailPath(t *testing.T) {
	t.Run("UpdateItem never writes SENT", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		_, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemSent)})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
	t.Run("a SENT item is final, paused or edited", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		sent, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		for name, in := range map[string]UpdateItemInput{
			"pause": {Status: ptr(ItemPaused)},
			"edit":  {BodyMD: ptr("rewrite history")},
			"date":  {ClearScheduledAt: true},
		} {
			in.ID, in.ExpectedRevision = sent.ID, sent.Revision
			_, err = f.svc.UpdateItem(ctx(), person, in)
			require.ErrorIs(t, err, ErrSentIsFinal, name)
		}
	})
}

// send is a SendItemEmail of it under its true approval hash.
func (f *fixture) send(it Item) SendItemEmailInput {
	return SendItemEmailInput{ItemID: it.ID, ExpectedRevision: it.Revision, ApprovalHash: ApprovalHash(it), Run: EmailRun{RunID: "run-1"}}
}

func sendsAs(id string) func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
	return func(context.Context, Actor, EmailRun, EmailMessage) (string, error) { return id, nil }
}

func TestApprovalHash(t *testing.T) {
	base := Item{ID: "item-1", Revision: 3, EmailTo: []string{"a@example.com", "b@example.com"}, EmailSubject: "News", BodyMD: "Hello"}
	h := ApprovalHash(base)
	assert.Len(t, h, 64)
	assert.Equal(t, h, ApprovalHash(base), "stable")
	// Pinned, computed independently of this code (Python: hashlib.sha256 over
	// struct.pack(">Q", len) + bytes per field). A format change must change
	// the version string and this value.
	assert.Equal(t, "323f0e14e9d515d2bfa8a3d121a465548619ae4657c8e652f77356f427f19396", h)

	for name, mut := range map[string]func(*Item){
		"id":         func(i *Item) { i.ID = "item-2" },
		"revision":   func(i *Item) { i.Revision = 4 },
		"recipient":  func(i *Item) { i.EmailTo = []string{"a@example.com"} },
		"order":      func(i *Item) { i.EmailTo = []string{"b@example.com", "a@example.com"} },
		"subject":    func(i *Item) { i.EmailSubject = "news" },
		"body":       func(i *Item) { i.BodyMD = "Hello!" },
		"boundaries": func(i *Item) { i.EmailSubject, i.BodyMD = "NewsHello", "" },
		"split addr": func(i *Item) { i.EmailTo = []string{"a@example.comb@example.com"} },
	} {
		other := base
		other.EmailTo = slices.Clone(base.EmailTo)
		mut(&other)
		assert.NotEqual(t, h, ApprovalHash(other), name)
	}
	ignored := base
	ignored.Title, ignored.ChannelID, ignored.CampaignID, ignored.PublishedURL = "x", "y", "z", "https://example.com"
	assert.Equal(t, h, ApprovalHash(ignored), "only what the card shows is bound")
}

func TestSendItemEmail(t *testing.T) {
	t.Run("sends the stored content once, as the actor, and records it", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		assert.Equal(t, "gmail-1", got.GmailMessageID)
		assert.Equal(t, ApprovalHash(it), got.ApprovalHash)
		assert.Equal(t, agent.UserID, got.SentBy)
		require.Equal(t, 1, f.email.CallCount("Send"))
		call := f.email.Calls("Send")[0]
		assert.Equal(t, agent, call.Args[1], "Email learns who sends")
		assert.Equal(t, EmailRun{RunID: "run-1"}, call.Args[2])
		assert.Equal(t, EmailMessage{To: []string{"a@example.com"}, Subject: "News", BodyMD: "Hello"}, call.Args[3])

		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.ErrorIs(t, err, ErrAlreadySent)
		assert.Equal(t, 1, f.email.CallCount("Send"), "never twice")
	})
	t.Run("refuses what is not an approved email at the approved revision", func(t *testing.T) {
		f := newFixture(t)
		review := f.emailItem(t, ItemReview)
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(review))
		require.ErrorIs(t, err, ErrNotApproved)
		post := f.itemAt(t, FormatLongPost, ItemApproved)
		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(post))
		require.ErrorIs(t, err, ErrNotApproved, "only an email is sent")
		approved := f.emailItem(t, ItemApproved)
		moved := f.send(approved)
		moved.ExpectedRevision--
		_, err = f.svc.SendItemEmail(ctx(), agent, moved)
		require.ErrorIs(t, err, ErrRevisionConflict)
		noRun := f.send(approved)
		noRun.Run = EmailRun{}
		_, err = f.svc.SendItemEmail(ctx(), agent, noRun)
		require.ErrorIs(t, err, ErrInvalidArgument, "a send needs the run's credential context")
		assert.Zero(t, f.email.CallCount("Send"))
	})
	t.Run("the approval hash must be the stored item's", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		for name, hash := range map[string]string{
			"made up":        "card-hash",
			"another item":   ApprovalHash(f.emailItem(t, ItemApproved)),
			"older revision": ApprovalHash(Item{ID: it.ID, Revision: it.Revision - 1, EmailTo: it.EmailTo, EmailSubject: it.EmailSubject, BodyMD: it.BodyMD}),
			"other content":  ApprovalHash(Item{ID: it.ID, Revision: it.Revision, EmailTo: it.EmailTo, EmailSubject: it.EmailSubject, BodyMD: "Hello, edited"}),
		} {
			in := f.send(it)
			in.ApprovalHash = hash
			_, err := f.svc.SendItemEmail(ctx(), agent, in)
			require.ErrorIs(t, err, ErrApprovalMismatch, name)
		}
		assert.Zero(t, f.email.CallCount("Send"))
		got, err := f.svc.GetItem(ctx(), person, GetItemInput{ID: it.ID})
		require.NoError(t, err)
		assert.Empty(t, got.ApprovalHash, "a refused send leaves no hold")
	})
	t.Run("no email client", func(t *testing.T) {
		_, err := New(Deps{Store: newFakeStore(), Gate: allowGate(orgA)}).SendItemEmail(ctx(), agent, SendItemEmailInput{ItemID: "x"})
		require.ErrorIs(t, err, ErrEmailUnavailable)
	})
	t.Run("a held item is frozen during the send, even for a person", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		var during []error
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			for _, in := range []UpdateItemInput{
				{ID: it.ID, ExpectedRevision: it.Revision, BodyMD: ptr("changed mid-send")},
				{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemPaused)},
				{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)},
				{ID: it.ID, ExpectedRevision: it.Revision, ScheduledAt: &t0},
			} {
				_, err := f.svc.UpdateItem(ctx(), person, in)
				during = append(during, err)
			}
			_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
			during = append(during, err)
			return "gmail-1", nil
		}
		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		assert.Equal(t, "Hello", got.BodyMD)
		require.Len(t, during, 5)
		for i, err := range during {
			var held *SendInDoubtError
			require.ErrorAs(t, err, &held, i)
			assert.Equal(t, it.ID, held.ItemID)
		}
		assert.Equal(t, 1, f.email.CallCount("Send"), "the concurrent send never reached Gmail")
	})
	t.Run("a send in doubt holds the item until a person resolves it", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			return "", errors.New("gmail: 502 from 10.0.0.7")
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		var doubt *SendInDoubtError
		require.ErrorAs(t, err, &doubt)
		assert.NotContains(t, err.Error(), "10.0.0.7", "the client's text never reaches the caller")
		assert.True(t, doubt.MayBeInFlight, "a send that just failed may still land")
		assert.Contains(t, err.Error(), "may still be in progress; wait a few minutes, then check the sent mail in Gmail and resolve")
		assert.NotContains(t, err.Error(), "re-approve")

		f.email.SendFunc = sendsAs("gmail-2")
		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.ErrorIs(t, err, ErrSendInDoubt, "a retry never resends")
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.ErrorIs(t, err, ErrSendInDoubt, "re-approving does not lift the hold")
		assert.Equal(t, 1, f.email.CallCount("Send"))

		hash := ApprovalHash(it)
		_, err = f.svc.ResolveInDoubtSend(ctx(), agent, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
		require.ErrorIs(t, err, ErrPersonOnly)
		_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: "other"})
		require.ErrorIs(t, err, ErrApprovalMismatch)

		// Too soon: the send may still be running.
		_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
		var flight *SendInFlightError
		require.ErrorAs(t, err, &flight)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.True(t, flight.ReleasableAt.Equal(t0.Add(2*EmailSendTimeout)))

		// "I checked Gmail; it did not go out."
		f.pastInFlight()
		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.ErrorAs(t, err, &doubt)
		assert.False(t, doubt.MayBeInFlight, "an old hold is no longer in flight")
		assert.Contains(t, err.Error(), "was started and not recorded")
		released, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, released.Status)
		assert.Empty(t, released.ApprovalHash)
		assert.Equal(t, it.Revision+1, released.Revision, "a release voids every earlier card")
		_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
		require.ErrorIs(t, err, ErrNotHeld)

		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(released))
		require.NoError(t, err)
		assert.Equal(t, "gmail-2", got.GmailMessageID)
	})
	t.Run("a release during the first send's flight never lets a second email out", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		hash := ApprovalHash(it)
		var during []error
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			// A duplicate send while the first is in flight.
			_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
			during = append(during, err)
			// A person releases the hold mid-flight.
			_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
			during = append(during, err)
			// And the duplicate tries again.
			_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
			during = append(during, err)
			return "gmail-1", nil
		}
		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		require.Len(t, during, 3)
		var doubt *SendInDoubtError
		require.ErrorAs(t, during[0], &doubt)
		assert.True(t, doubt.MayBeInFlight)
		assert.Contains(t, during[0].Error(), "may still be in progress; wait a few minutes")
		require.ErrorIs(t, during[1], ErrSendMayBeInFlight, "no release while the send may be running")
		require.ErrorIs(t, during[2], ErrSendInDoubt)
		assert.Equal(t, 1, f.email.CallCount("Send"), "exactly one Gmail send")
	})
	t.Run("even a release after the in-flight age leaves the in-flight card unable to resend", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		hash := ApprovalHash(it)
		var during []error
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			f.pastInFlight() // a send stuck far past its timeout
			_, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: hash})
			during = append(during, err)
			_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
			during = append(during, err)
			return "gmail-1", nil
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		var su *SentUnrecordedError
		require.ErrorAs(t, err, &su, "its hold was released under it, so it is not recorded")
		require.Len(t, during, 2)
		require.NoError(t, during[0])
		require.ErrorIs(t, during[1], ErrRevisionConflict, "the old card is void")
		assert.Equal(t, 1, f.email.CallCount("Send"), "exactly one Gmail send")
	})
	t.Run("after a timed-out send and a release, only a new card sends", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		original := f.send(it)
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			return "", context.DeadlineExceeded
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, original)
		require.ErrorIs(t, err, ErrSendInDoubt)

		f.pastInFlight()
		released, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: original.ApprovalHash})
		require.NoError(t, err)

		f.email.SendFunc = sendsAs("gmail-new")
		_, err = f.svc.SendItemEmail(ctx(), agent, original)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRevisionConflict) || errors.Is(err, ErrApprovalMismatch), "the replayed card is refused: %v", err)
		stale := original
		stale.ExpectedRevision = released.Revision // the old hash at the new revision
		_, err = f.svc.SendItemEmail(ctx(), agent, stale)
		require.ErrorIs(t, err, ErrApprovalMismatch)
		assert.Equal(t, 1, f.email.CallCount("Send"), "exactly one send before the new card")

		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(released))
		require.NoError(t, err)
		assert.Equal(t, "gmail-new", got.GmailMessageID)
		assert.Equal(t, 2, f.email.CallCount("Send"))
	})
	t.Run("a person records an in-doubt send found in Gmail", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			return "", context.DeadlineExceeded
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.ErrorIs(t, err, ErrSendInDoubt)
		_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: ApprovalHash(it), GmailMessageID: ptr("")})
		require.ErrorIs(t, err, ErrInvalidArgument)
		got, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: ApprovalHash(it), GmailMessageID: ptr("gmail-found")})
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		assert.Equal(t, "gmail-found", got.GmailMessageID)
		assert.Equal(t, person.UserID, got.SentBy)
		_, err = f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: ApprovalHash(it), GmailMessageID: ptr("again")})
		require.ErrorIs(t, err, ErrAlreadySent)
	})
	t.Run("ResolveInDoubtSend needs a hold", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		_, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: ApprovalHash(it), GmailMessageID: ptr("fake")})
		require.ErrorIs(t, err, ErrNotHeld, "no SENT without a send")
	})
	t.Run("a send that certainly failed releases the hold", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			return "", fmt.Errorf("%w: connection conn-7 belongs to another user", ErrEmailNotSent)
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.ErrorIs(t, err, ErrEmailNotSent)
		require.NotErrorIs(t, err, ErrSendInDoubt)
		assert.Equal(t, "the email was not sent", err.Error(), "a fixed message")

		f.email.SendFunc = sendsAs("gmail-3")
		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		assert.Equal(t, "gmail-3", got.GmailMessageID)
	})
	t.Run("a sent email whose record fails names the message, is not a conflict, and is never resent", func(t *testing.T) {
		for name, recErr := range map[string]error{
			"store failure": errors.New("connection reset"),
			"conflict":      ErrRevisionConflict,
		} {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				it := f.emailItem(t, ItemApproved)
				f.store.failUpdateItem = recErr
				_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
				var su *SentUnrecordedError
				require.ErrorAs(t, err, &su)
				assert.Equal(t, "gmail-1", su.GmailMessageID)
				assert.ErrorIs(t, err, ErrSentUnrecorded)
				assert.ErrorIs(t, err, svcerr.ErrDataLoss)
				assert.NotErrorIs(t, err, ErrRevisionConflict)
				assert.NotErrorIs(t, err, svcerr.ErrAborted, "never retryable")
				assert.Contains(t, err.Error(), "gmail-1")

				_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it))
				require.ErrorIs(t, err, ErrSendInDoubt)
				assert.Equal(t, 1, f.email.CallCount("Send"))

				got, err := f.svc.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: it.ID, ApprovalHash: ApprovalHash(it), GmailMessageID: ptr("gmail-1")})
				require.NoError(t, err)
				assert.Equal(t, ItemSent, got.Status)
			})
		}
	})
	t.Run("Send is bounded by the send timeout", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		var deadline time.Time
		f.email.SendFunc = func(ctx context.Context, _ Actor, _ EmailRun, _ EmailMessage) (string, error) {
			deadline, _ = ctx.Deadline()
			return "gmail-1", nil
		}
		start := time.Now()
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(it))
		require.NoError(t, err)
		assert.WithinDuration(t, start.Add(EmailSendTimeout), deadline, 5*time.Second, "the default is 60s")
		assert.Equal(t, 60*time.Second, EmailSendTimeout)

		it2 := f.emailItem(t, ItemApproved)
		f.svc.(*service).sendTimeout = 20 * time.Millisecond
		f.email.SendFunc = func(ctx context.Context, _ Actor, _ EmailRun, _ EmailMessage) (string, error) {
			<-ctx.Done() // a Gmail that never answers
			return "", ctx.Err()
		}
		start = time.Now()
		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(it2))
		require.ErrorIs(t, err, ErrSendInDoubt, "a timeout may have sent")
		assert.Less(t, time.Since(start), 5*time.Second)
	})
	t.Run("the record lands although the caller gave up after the send", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		callCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.email.SendFunc = func(context.Context, Actor, EmailRun, EmailMessage) (string, error) {
			cancel() // the caller hangs up while Gmail answers
			return "gmail-1", nil
		}
		got, err := f.svc.SendItemEmail(callCtx, agent, f.send(it))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		stored, err := f.svc.GetItem(ctx(), person, GetItemInput{ID: it.ID})
		require.NoError(t, err)
		assert.Equal(t, ItemSent, stored.Status)
	})
}

func TestGetCampaignCapsItsLists(t *testing.T) {
	for name, n := range map[string]int{"under": 200, "over": 250} {
		t.Run(name, func(t *testing.T) {
			var ideaLimit, itemLimit int
			st := &MockStore{
				GetCampaignFunc: func(_ context.Context, org, id string) (*store.GtmCampaign, error) {
					return &store.GtmCampaign{ID: id, OrgID: org}, nil
				},
				ListIdeasFunc: func(_ context.Context, f IdeaFilter) ([]*store.GtmIdea, error) {
					ideaLimit = f.Limit
					return nil, nil
				},
				CampaignItemCountsFunc: func(context.Context, string, string) ([]ItemCount, error) { return nil, nil },
				ListItemsFunc: func(_ context.Context, f ItemFilter) ([]*store.GtmItem, error) {
					itemLimit = f.Limit
					rows := make([]*store.GtmItem, 0, n)
					for i := range min(n, f.Limit) {
						rows = append(rows, &store.GtmItem{ID: fmt.Sprint(i), OrgID: orgA})
					}
					return rows, nil
				},
			}
			d, err := New(Deps{Store: st, Gate: allowGate(orgA)}).GetCampaign(ctx(), person, GetCampaignInput{ID: "c"})
			require.NoError(t, err)
			assert.Equal(t, MaxCampaignIdeas+1, ideaLimit)
			assert.Equal(t, MaxCampaignItems+1, itemLimit)
			assert.Equal(t, 200, MaxCampaignItems)
			assert.Len(t, d.Items, 200)
			assert.Equal(t, n > 200, d.Truncated)
		})
	}
}

// A missing row and a gate refusal are the same answer on the wire: the
// same Connect code and the same message, byte for byte, for every entity.
func TestMissingRowsReadLikeRefusals(t *testing.T) {
	refused := New(Deps{Store: newFakeStore(), Gate: allowGate("another-org")})
	f := newFixture(t)
	calls := map[string]func(Service) error{
		"idea": func(s Service) error { _, err := s.GetIdea(ctx(), person, GetIdeaInput{ID: "nope"}); return err },
		"idea update": func(s Service) error {
			_, err := s.UpdateIdea(ctx(), person, UpdateIdeaInput{ID: "nope", ExpectedRevision: 1, Title: ptr("x")})
			return err
		},
		"idea status": func(s Service) error {
			_, err := s.SetIdeaStatus(ctx(), person, SetIdeaStatusInput{ID: "nope", ExpectedRevision: 1, Status: IdeaAccepted})
			return err
		},
		"campaign": func(s Service) error {
			_, err := s.GetCampaign(ctx(), person, GetCampaignInput{ID: "nope"})
			return err
		},
		"campaign update": func(s Service) error {
			_, err := s.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: "nope", ExpectedRevision: 1, Name: ptr("x")})
			return err
		},
		"item": func(s Service) error { _, err := s.GetItem(ctx(), person, GetItemInput{ID: "nope"}); return err },
		"item update": func(s Service) error {
			_, err := s.UpdateItem(ctx(), person, UpdateItemInput{ID: "nope", ExpectedRevision: 1})
			return err
		},
		"item send": func(s Service) error {
			_, err := s.SendItemEmail(ctx(), person, SendItemEmailInput{ItemID: "nope", ExpectedRevision: 1, ApprovalHash: "h", Run: EmailRun{RunID: "r"}})
			return err
		},
		"item resolve": func(s Service) error {
			_, err := s.ResolveInDoubtSend(ctx(), person, ResolveInDoubtSendInput{ItemID: "nope", ApprovalHash: "h"})
			return err
		},
		"claim": func(s Service) error {
			_, err := s.UpsertClaim(ctx(), person, UpsertClaimInput{ID: "nope", ExpectedRevision: 1, Notes: ptr("x")})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			refusal, missing := call(refused), call(f.svc)
			require.ErrorIs(t, refusal, ErrNotFound)
			require.ErrorIs(t, missing, ErrNotFound)
			rc, mc := svcerr.ToConnect(refusal), svcerr.ToConnect(missing)
			assert.Equal(t, rc.Code(), mc.Code())
			assert.Equal(t, connect.CodeNotFound, mc.Code())
			assert.Equal(t, rc.Message(), mc.Message())
			assert.Equal(t, rc.Error(), mc.Error())
		})
	}
}
