// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"testing"
	"time"

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
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		store: newFakeStore(),
		email: &MockEmail{SendFunc: func(context.Context, EmailRun, EmailMessage) (string, error) {
			return "gmail-1", nil
		}},
		analytics: &MockAnalytics{QueryFunc: func(_ context.Context, _ string, q AnalyticsQuery) (AnalyticsResult, error) {
			return AnalyticsResult{Template: q.Template, From: q.From, To: q.To}, nil
		}},
	}
	f.svc = New(Deps{
		Store:     f.store,
		Gate:      allowGate(orgA, orgB),
		Clock:     &MockClock{NowFunc: func() time.Time { return t0 }},
		Analytics: f.analytics,
		Email:     f.email,
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
		Kind: ItemEmail, Title: "Launch mail", BodyMD: "Hello", EmailTo: []string{"a@example.com"}, EmailSubject: "News",
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
			_, err := s.CreateItem(ctx(), a, CreateItemInput{Kind: ItemBlogPost, Title: "t"})
			return err
		},
		"UpdateItem": func(s Service, a Actor) error {
			_, err := s.UpdateItem(ctx(), a, UpdateItemInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"MarkItemSent": func(s Service, a Actor) error {
			_, err := s.MarkItemSent(ctx(), a, MarkItemSentInput{ItemID: "x", ExpectedRevision: 1, GmailMessageID: "m", ApprovalHash: "h"})
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
	t.Run("a gate error keeps its cause server-side", func(t *testing.T) {
		g := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return false, gateErr }}
		_, err := New(Deps{Store: &MockStore{}, Gate: g}).ListIdeas(ctx(), person, ListIdeasInput{})
		require.ErrorIs(t, err, ErrNotFound)
		assert.ErrorIs(t, svcerr.Cause(err), gateErr)
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
	c, err := f.svc.CreateCampaign(ctx(), other, CreateCampaignInput{Name: "B's"})
	require.NoError(t, err)
	bIdea, err := f.svc.CreateIdea(ctx(), other, CreateIdeaInput{Kind: IdeaKindGTM, Title: "B's idea"})
	require.NoError(t, err)

	var link *LinkError
	_, err = f.svc.CreateIdea(ctx(), person, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t", CampaignID: c.ID})
	require.ErrorAs(t, err, &link)
	assert.Equal(t, "campaign_id", link.Field)
	assert.ErrorIs(t, err, svcerr.ErrInvalidArgument)

	_, err = f.svc.CreateItem(ctx(), person, CreateItemInput{Kind: ItemBlogPost, Title: "t", IdeaID: bIdea.ID})
	require.ErrorAs(t, err, &link)
	assert.Equal(t, "idea_id", link.Field)

	it, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Kind: ItemBlogPost, Title: "t"})
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
		_, err := svc.CreateItem(ctx(), person, CreateItemInput{Kind: ItemBlogPost, Title: "t", CampaignID: "c"})
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
		for _, st := range []ItemStatus{ItemScheduled, ItemPublished, ItemPaused} {
			_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(st)})
			require.ErrorIs(t, err, ErrPersonOnly, st)
		}
		got, err := f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemReview), BodyMD: ptr("v2")})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status)
	})
	t.Run("an agent touches DRAFT and REVIEW items only", func(t *testing.T) {
		f := newFixture(t)
		when := t0.Add(48 * time.Hour)
		for _, st := range []ItemStatus{ItemApproved, ItemScheduled, ItemPublished, ItemPaused} {
			it := f.itemAt(t, ItemBlogPost, st)
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
		it := f.itemAt(t, ItemBlogPost, ItemReview)
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
		it, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemSocialPost, Title: "t"})
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

func TestSentIsWrittenOnlyByTheEmailPath(t *testing.T) {
	t.Run("UpdateItem never writes SENT", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		_, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemSent)})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
	t.Run("MarkItemSent preconditions", func(t *testing.T) {
		f := newFixture(t)
		rec := func(it Item) MarkItemSentInput {
			return MarkItemSentInput{ItemID: it.ID, ExpectedRevision: it.Revision, GmailMessageID: "m-1", ApprovalHash: "h-1"}
		}
		draft := f.emailItem(t, ItemReview)
		_, err := f.svc.MarkItemSent(ctx(), person, rec(draft))
		require.ErrorIs(t, err, ErrNotApproved)

		post, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Kind: ItemBlogPost, Title: "Post"})
		require.NoError(t, err)
		post, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: post.ID, ExpectedRevision: post.Revision, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		_, err = f.svc.MarkItemSent(ctx(), person, rec(post))
		require.ErrorIs(t, err, ErrNotApproved, "only an email is sent")

		ok := f.emailItem(t, ItemApproved)
		stale := rec(ok)
		stale.ExpectedRevision--
		_, err = f.svc.MarkItemSent(ctx(), person, stale)
		require.ErrorIs(t, err, ErrRevisionConflict)

		_, err = f.svc.MarkItemSent(ctx(), person, MarkItemSentInput{ItemID: ok.ID, ExpectedRevision: ok.Revision, ApprovalHash: "h"})
		require.ErrorIs(t, err, ErrInvalidArgument, "a send record needs the gmail id")

		sent, err := f.svc.MarkItemSent(ctx(), agent, rec(ok))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, sent.Status)
		assert.Equal(t, "m-1", sent.GmailMessageID)
		assert.Equal(t, "h-1", sent.ApprovalHash)
		assert.Equal(t, agent.UserID, sent.SentBy)
		require.NotNil(t, sent.SentAt)

		_, err = f.svc.MarkItemSent(ctx(), person, rec(ok))
		require.ErrorIs(t, err, ErrAlreadySent, "a send is recorded once")
		_, err = f.svc.MarkItemSent(ctx(), person, rec(sent))
		require.ErrorIs(t, err, ErrAlreadySent)
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: sent.ID, ExpectedRevision: sent.Revision, BodyMD: ptr("rewrite history")})
		require.ErrorIs(t, err, ErrSentIsFinal)
	})
}

func TestSendItemEmail(t *testing.T) {
	run := EmailRun{RunID: "run-1"}
	send := func(it Item) SendItemEmailInput {
		return SendItemEmailInput{ItemID: it.ID, ExpectedRevision: it.Revision, ApprovalHash: "card-hash", Run: run}
	}
	t.Run("sends the stored content once and records it", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		got, err := f.svc.SendItemEmail(ctx(), agent, send(it))
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
		assert.Equal(t, "gmail-1", got.GmailMessageID)
		assert.Equal(t, "card-hash", got.ApprovalHash)
		require.Equal(t, 1, f.email.CallCount("Send"))
		call := f.email.Calls("Send")[0]
		assert.Equal(t, run, call.Args[1])
		assert.Equal(t, EmailMessage{To: []string{"a@example.com"}, Subject: "News", BodyMD: "Hello"}, call.Args[2])

		_, err = f.svc.SendItemEmail(ctx(), agent, send(it))
		require.ErrorIs(t, err, ErrAlreadySent)
		assert.Equal(t, 1, f.email.CallCount("Send"), "never twice")
	})
	t.Run("refuses what is not an approved email at the approved revision", func(t *testing.T) {
		f := newFixture(t)
		review := f.emailItem(t, ItemReview)
		_, err := f.svc.SendItemEmail(ctx(), agent, send(review))
		require.ErrorIs(t, err, ErrNotApproved)
		approved := f.emailItem(t, ItemApproved)
		moved := send(approved)
		moved.ExpectedRevision--
		_, err = f.svc.SendItemEmail(ctx(), agent, moved)
		require.ErrorIs(t, err, ErrRevisionConflict)
		_, err = f.svc.SendItemEmail(ctx(), agent, SendItemEmailInput{ItemID: approved.ID, ExpectedRevision: approved.Revision, ApprovalHash: "h"})
		require.ErrorIs(t, err, ErrInvalidArgument, "a send needs the run's credential context")
		assert.Zero(t, f.email.CallCount("Send"))
	})
	t.Run("no email client", func(t *testing.T) {
		_, err := New(Deps{Store: newFakeStore(), Gate: allowGate(orgA)}).SendItemEmail(ctx(), agent, SendItemEmailInput{ItemID: "x"})
		require.ErrorIs(t, err, ErrEmailUnavailable)
	})
	t.Run("a send in doubt holds the item until a person re-approves", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.email.SendFunc = func(context.Context, EmailRun, EmailMessage) (string, error) {
			return "", context.DeadlineExceeded
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, send(it))
		require.ErrorIs(t, err, ErrSendInDoubt)

		f.email.SendFunc = func(context.Context, EmailRun, EmailMessage) (string, error) { return "gmail-2", nil }
		_, err = f.svc.SendItemEmail(ctx(), agent, send(it))
		require.ErrorIs(t, err, ErrSendInDoubt, "a retry never resends")
		assert.Equal(t, 1, f.email.CallCount("Send"))

		// An agent cannot release it.
		_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.ErrorIs(t, err, ErrPersonOnly)

		// A person's re-approval does.
		re, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		assert.Empty(t, re.ApprovalHash)
		got, err := f.svc.SendItemEmail(ctx(), agent, send(re))
		require.NoError(t, err)
		assert.Equal(t, "gmail-2", got.GmailMessageID)
	})
	t.Run("a send that certainly failed releases the hold", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.email.SendFunc = func(context.Context, EmailRun, EmailMessage) (string, error) {
			return "", svcerr.WithCause(ErrEmailNotSent, errors.New("no gmail connection"))
		}
		_, err := f.svc.SendItemEmail(ctx(), agent, send(it))
		require.ErrorIs(t, err, ErrEmailNotSent)
		require.NotErrorIs(t, err, ErrSendInDoubt)

		f.email.SendFunc = func(context.Context, EmailRun, EmailMessage) (string, error) { return "gmail-3", nil }
		got, err := f.svc.SendItemEmail(ctx(), agent, send(it))
		require.NoError(t, err)
		assert.Equal(t, "gmail-3", got.GmailMessageID)
	})
	t.Run("a sent email whose record fails names the message and is never resent", func(t *testing.T) {
		f := newFixture(t)
		it := f.emailItem(t, ItemApproved)
		f.store.failUpdateItem = errors.New("connection reset")
		_, err := f.svc.SendItemEmail(ctx(), agent, send(it))
		var su *SentUnrecordedError
		require.ErrorAs(t, err, &su)
		assert.Equal(t, "gmail-1", su.GmailMessageID)
		assert.ErrorIs(t, err, svcerr.ErrDataLoss)
		assert.Contains(t, err.Error(), "gmail-1")

		_, err = f.svc.SendItemEmail(ctx(), agent, send(it))
		require.ErrorIs(t, err, ErrSendInDoubt)
		assert.Equal(t, 1, f.email.CallCount("Send"))

		// A person who finds it in the sent mail records it under the same card.
		got, err := f.svc.MarkItemSent(ctx(), person, MarkItemSentInput{ItemID: it.ID, ExpectedRevision: it.Revision, GmailMessageID: "gmail-1", ApprovalHash: "card-hash"})
		require.NoError(t, err)
		assert.Equal(t, ItemSent, got.Status)
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
		"window too long":        {Template: AnalyticsEventCounts, From: t0.Add(-91 * 24 * time.Hour), To: t0},
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
		_, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemSocialPost, Title: "in", ScheduledAt: &in})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemSocialPost, Title: "out", ScheduledAt: &out})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemSocialPost, Title: "undated"})
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
	c, err := f.svc.CreateCampaign(ctx(), agent, CreateCampaignInput{Name: "Q1 launch", Goal: "100 signups"})
	require.NoError(t, err)
	assert.Equal(t, CampaignDraft, c.Status)
	_, err = f.svc.CreateIdea(ctx(), agent, CreateIdeaInput{Kind: IdeaKindGTM, Title: "Launch post", CampaignID: c.ID})
	require.NoError(t, err)
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemBlogPost, Title: "Post", CampaignID: c.ID})
	require.NoError(t, err)
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemBlogPost, Title: "Unlinked"})
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
	_, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemBlogPost, Title: "t", EmailTo: []string{"a@example.com"}})
	require.ErrorIs(t, err, ErrInvalidArgument, "only an email has recipients")
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemEmail, Title: "t", EmailTo: []string{"Bob <b@example.com>"}})
	require.ErrorIs(t, err, ErrInvalidArgument, "bare addresses only")
	_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Kind: ItemEmail, Title: "  "})
	require.ErrorIs(t, err, ErrInvalidArgument)

	it := f.itemAt(t, ItemBlogPost, ItemApproved)
	_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemScheduled)})
	require.ErrorIs(t, err, ErrInvalidArgument, "a scheduled item needs a date")
}

// itemAt makes an item of kind and walks it, as a person, to status.
func (f *fixture) itemAt(t *testing.T, kind ItemKind, status ItemStatus) Item {
	t.Helper()
	in := CreateItemInput{Kind: kind, Title: "Item", BodyMD: "Body"}
	if kind == ItemEmail {
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
	allowed := func(kind ItemKind, from, to ItemStatus) bool {
		email := kind == ItemEmail
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
	for _, kind := range []ItemKind{ItemBlogPost, ItemEmail} {
		for _, from := range all {
			if kind == ItemEmail && (from == ItemScheduled || from == ItemPublished) {
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
			it := f.itemAt(t, ItemBlogPost, st)
			got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Title: ptr("New title")})
			require.NoError(t, err)
			assert.Equal(t, ItemReview, got.Status, st)

			it = f.itemAt(t, ItemBlogPost, st)
			got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, BodyMD: ptr("New"), Status: ptr(ItemApproved)})
			require.NoError(t, err, "edit and re-approve in one call")
			assert.Equal(t, ItemApproved, got.Status)
		}
		it := f.itemAt(t, ItemBlogPost, ItemApproved)
		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Title: ptr(it.Title), Channel: ptr(it.Channel)})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status, "the same content is no change")
		email := f.itemAt(t, ItemEmail, ItemApproved)
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: email.ID, ExpectedRevision: email.Revision, EmailTo: &[]string{"b@example.com"}})
		require.NoError(t, err)
		assert.Equal(t, ItemReview, got.Status, "recipients are content")
	})
	t.Run("a person dates an approved email and it stays APPROVED", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, ItemEmail, ItemApproved)
		when := t0.Add(72 * time.Hour)
		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, ScheduledAt: &when})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status)
		assert.True(t, got.ScheduledAt.Equal(when))
	})
	t.Run("an email cannot become SCHEDULED or PUBLISHED by changing kind", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, ItemBlogPost, ItemPublished)
		_, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{
			ID: it.ID, ExpectedRevision: it.Revision, Kind: ptr(ItemEmail),
		})
		require.ErrorIs(t, err, ErrInvalidTransition)
	})
}

func TestGetCampaignCapsItsLists(t *testing.T) {
	var ideaLimit, itemLimit int
	st := &MockStore{
		GetCampaignFunc: func(_ context.Context, org, id string) (*store.GtmCampaign, error) {
			return &store.GtmCampaign{ID: id, OrgID: org}, nil
		},
		ListIdeasFunc: func(_ context.Context, f IdeaFilter) ([]*store.GtmIdea, error) {
			ideaLimit = f.Limit
			return nil, nil
		},
		ListItemsFunc: func(_ context.Context, f ItemFilter) ([]*store.GtmItem, error) {
			itemLimit = f.Limit
			return nil, nil
		},
	}
	_, err := New(Deps{Store: st, Gate: allowGate(orgA)}).GetCampaign(ctx(), person, GetCampaignInput{ID: "c"})
	require.NoError(t, err)
	assert.Equal(t, 200, ideaLimit)
	assert.Equal(t, 200, itemLimit)
}
