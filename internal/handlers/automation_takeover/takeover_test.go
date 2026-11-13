package automation_takeover

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
)

// The people of these tests, all about the org acme: ada administers it, bob
// and carol are members, liv is a member who made automations, cora made
// automations and has left, olga was never in it.
const (
	ada, bob, carol, liv, cora, olga = "ada", "bob", "carol", "liv", "cora", "olga"
	acme                             = "acme"
)

// members is control-plane's answer about acme, through authz's Membership
// and the fresh role read (orgs.Resolver.RoleFresh's shape). roleUnknown is a
// control plane that names no role.
type members struct {
	mu          sync.Mutex
	in          map[string]bool
	roles       map[string]string
	roleUnknown bool
	roleAsks    int
}

func newMembers() *members {
	return &members{
		in:    map[string]bool{ada: true, bob: true, carol: true, liv: true},
		roles: map[string]string{ada: "admin", bob: "member", carol: "member", liv: "member"},
	}
}

func (m *members) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return orgID == acme && m.in[userID], true, nil
}

func (m *members) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roleAsks++
	if m.roleUnknown {
		return "", false, nil
	}
	if orgID != acme || !m.in[userID] {
		return "", true, nil
	}
	return m.roles[userID], true, nil
}

func (m *members) set(userID string, member bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in[userID] = member
}

// noRoles is a Membership that cannot name roles (a resolver before S0).
type noRoles struct{ m *members }

func (n noRoles) IsMember(ctx context.Context, userID, orgID string) (bool, bool, error) {
	return n.m.IsMember(ctx, userID, orgID)
}

// fakeRepo keeps triggers, saved workflows and offers in memory.
type fakeRepo struct {
	mu       sync.Mutex
	triggers map[string]*core.Trigger
	drafts   []*core.WorkflowDraft
	offers   map[string]*core.TriggerOffer
}

func (r *fakeRepo) GetTrigger(_ context.Context, id string) (*core.Trigger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.triggers[id]
	if !ok {
		return nil, core.ErrTriggerNotFound
	}
	c := *t
	return &c, nil
}

func (r *fakeRepo) ListOrgTriggers(_ context.Context, orgID string) ([]*core.Trigger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.Trigger
	for _, t := range r.triggers {
		if t.OrgID != nil && *t.OrgID == orgID {
			c := *t
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *fakeRepo) ListOrgWorkflowDrafts(_ context.Context, orgID string) ([]*core.WorkflowDraft, error) {
	var out []*core.WorkflowDraft
	for _, d := range r.drafts {
		if d.OrgID != nil && *d.OrgID == orgID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *fakeRepo) CreateTriggerOffer(_ context.Context, o *core.TriggerOffer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, prev := range r.offers {
		if prev.TriggerID == o.TriggerID && prev.Status == core.TriggerOfferPending {
			prev.Status, prev.StatusReason = core.TriggerOfferRevoked, core.TriggerOfferRevokedSuperseded
		}
	}
	c := *o
	r.offers[o.ID] = &c
	return nil
}

func (r *fakeRepo) GetTriggerOffer(_ context.Context, id string) (*core.TriggerOffer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.offers[id]
	if !ok {
		return nil, core.ErrTriggerOfferNotFound
	}
	c := *o
	return &c, nil
}

func (r *fakeRepo) ListPendingTriggerOffers(_ context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.TriggerOffer
	for _, o := range r.offers {
		if o.Status != core.TriggerOfferPending ||
			(f.OffereeUserID != "" && o.OffereeUserID != f.OffereeUserID) ||
			(f.OrgID != nil && o.OrgID != *f.OrgID) {
			continue
		}
		if f.TriggerIDs != nil {
			found := false
			for _, id := range f.TriggerIDs {
				found = found || id == o.TriggerID
			}
			if !found {
				continue
			}
		}
		c := *o
		out = append(out, &c)
	}
	return out, nil
}

func (r *fakeRepo) SettleTriggerOffer(_ context.Context, id, status, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.offers[id]
	if !ok || o.Status != core.TriggerOfferPending {
		return false, nil
	}
	o.Status, o.StatusReason = status, reason
	return true, nil
}

func (r *fakeRepo) offer(id string) core.TriggerOffer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.offers[id]
}

// adoption is one AdoptAs call.
type adoption struct {
	triggerID, adopter, offerID string
}

// fakeTriggers is the trigger service's half: it re-owns the row in the fake
// repo (and accepts the offer), and renders a definition marked as such.
type fakeTriggers struct {
	repo    *fakeRepo
	mu      sync.Mutex
	adopted []adoption
}

func (f *fakeTriggers) AdoptAs(_ context.Context, t *core.Trigger, adopterID string, _ *pb.TriggerRebinding, offerID string) (*pb.AcceptTriggerOfferResponse, error) {
	f.mu.Lock()
	f.adopted = append(f.adopted, adoption{t.ID, adopterID, offerID})
	f.mu.Unlock()
	f.repo.mu.Lock()
	f.repo.triggers[t.ID].UserID = adopterID
	f.repo.triggers[t.ID].Enabled = false
	if o := f.repo.offers[offerID]; o != nil {
		o.Status = core.TriggerOfferAccepted
	}
	f.repo.mu.Unlock()
	return &pb.AcceptTriggerOfferResponse{Trigger: &pb.Trigger{Id: t.ID}, Notices: []string{"paused"}}, nil
}

func (f *fakeTriggers) RenderDefinition(_ context.Context, t *core.Trigger) (*pb.Trigger, []string) {
	names := make([]string, 0, len(t.Params))
	for n := range t.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	return &pb.Trigger{Id: t.ID, Name: t.Name + " (definition)"}, names
}

func (f *fakeTriggers) calls() []adoption {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adoption(nil), f.adopted...)
}

type takeoverEnv struct {
	svc      *Service
	repo     *fakeRepo
	triggers *fakeTriggers
	members  *members
	now      time.Time
}

func setupTakeover(t *testing.T) *takeoverEnv {
	t.Helper()
	org := acme
	repo := &fakeRepo{
		triggers: map[string]*core.Trigger{
			"orphan": {ID: "orphan", UserID: cora, OrgID: &org, Name: "nightly", Params: map[string]any{"api_token": "hunter2"}},
			"livs":   {ID: "livs", UserID: liv, OrgID: &org, Name: "livs nightly"},
		},
		drafts: []*core.WorkflowDraft{
			{ID: "cora-wf", UserID: cora, OrgID: &org, Name: "release", Slug: "release", Definition: "name: release"},
			{ID: "liv-wf", UserID: liv, OrgID: &org, Name: "mine", Slug: "mine"},
		},
		offers: map[string]*core.TriggerOffer{},
	}
	e := &takeoverEnv{repo: repo, triggers: &fakeTriggers{repo: repo}, members: newMembers(),
		now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	e.svc = NewTestAutomationTakeover(t, WithAutomationTakeoverDeps(Deps{
		Repo: repo, Triggers: e.triggers, Now: func() time.Time { return e.now },
	}))
	return e
}

// as is userID signed in to the app, deciding with m.
func as(userID string, m authz.Membership) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return authz.WithMembership(auth.WithCredential(ctx, auth.CredentialSession), m)
}

func (e *takeoverEnv) as(userID string) context.Context { return as(userID, e.members) }

func reason(err error) string {
	return svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader)
}

func (e *takeoverEnv) list(ctx context.Context) (*pb.ListOrphanedAutomationsResponse, error) {
	org := acme
	resp, err := e.svc.ListOrphanedAutomations(ctx, connect.NewRequest(&pb.ListOrphanedAutomationsRequest{OrgId: &org}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *takeoverEnv) offer(t *testing.T, triggerID, to string) *pb.TriggerOffer {
	t.Helper()
	resp, err := e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{TriggerId: triggerID, NewOwnerUserId: to}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetOffer())
	return resp.Msg.GetOffer()
}

func (e *takeoverEnv) accept(ctx context.Context, offerID string) error {
	_, err := e.svc.AcceptTriggerOffer(ctx, connect.NewRequest(&pb.AcceptTriggerOfferRequest{
		OfferId: offerID, Rebinding: &pb.TriggerRebinding{ProjectId: "p", DaemonId: "d"},
	}))
	return err
}

// Only an org admin, by a FRESH read of their role, signed in as a person,
// reaches a takeover. A role control-plane cannot name (an older control
// plane) is refused; so is a membership that cannot read roles at all, and a
// machine or agent credential.
func TestTakeoverNeedsAFreshOrgAdminWhoIsAPerson(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	unknown := newMembers()
	unknown.roleUnknown = true
	machine := auth.WithCredential(context.WithValue(context.Background(), auth.UserIDContextKey, ada), auth.CredentialAccessToken)
	cases := []struct {
		name   string
		ctx    context.Context
		code   connect.Code
		reason string
	}{
		{"an admin, by a role read now", e.as(ada), 0, ""},
		{"a member", e.as(bob), connect.CodePermissionDenied, orgtakeover.ReasonOrgAdminRequired},
		{"someone not in the org", e.as(olga), connect.CodePermissionDenied, orgtakeover.ReasonOrgAdminRequired},
		{"an admin whose role is unknown", as(ada, unknown), connect.CodePermissionDenied, orgtakeover.ReasonOrgAdminRequired},
		{"a membership that cannot read roles", as(ada, noRoles{e.members}), connect.CodeUnavailable, ""},
		{"a machine or agent credential", authz.WithMembership(machine, e.members), connect.CodePermissionDenied, auth.ReasonPersonRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.list(tc.ctx)
			if tc.code == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "error: %v", err)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, reason(err))
			}
		})
	}

	// The role is never cached: two decisions, two reads.
	before := e.members.roleAsks
	_, _ = e.list(e.as(ada))
	_, _ = e.list(e.as(ada))
	assert.Equal(t, before+2, e.members.roleAsks)

	// A just-demoted admin is refused at once.
	e.members.mu.Lock()
	e.members.roles[ada] = "member"
	e.members.mu.Unlock()
	_, err := e.list(e.as(ada))
	assert.Equal(t, orgtakeover.ReasonOrgAdminRequired, reason(err))
}

// The orphan list shows an admin only the automations whose creator left,
// with their definitions as RenderDefinition renders them (values withheld)
// and their pending offer. A live member's automations are not orphans and
// are not shown. Nobody but an admin sees the list at all.
func TestListOrphanedAutomations_OnlyOrphansAndOnlyToAdmins(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	offer := e.offer(t, "orphan", bob)

	got, err := e.list(e.as(ada))
	require.NoError(t, err)
	require.Len(t, got.GetTriggers(), 1)
	orphan := got.GetTriggers()[0]
	assert.Equal(t, "orphan", orphan.GetTrigger().GetId())
	assert.Equal(t, "nightly (definition)", orphan.GetTrigger().GetName(), "rendered by RenderDefinition, values withheld")
	assert.Equal(t, []string{"api_token"}, orphan.GetParamNames())
	assert.NotContains(t, orphan.String(), "hunter2")
	assert.Equal(t, cora, orphan.GetCreatorUserId())
	assert.Equal(t, offer.GetId(), orphan.GetPendingOffer().GetId())
	require.Len(t, got.GetWorkflows(), 1)
	assert.Equal(t, "cora-wf", got.GetWorkflows()[0].GetId())

	for _, who := range []string{bob, liv, cora, olga} {
		_, err := e.list(e.as(who))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%s sees no orphans", who)
	}
}

// An admin never pushes an automation onto another member: naming them makes
// an offer, nothing is re-owned, and only that member's accept adopts it.
func TestAdminCannotAdoptOntoAnotherMemberWithoutTheirAccept(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)

	// The admin may not choose the member's project or machine for them.
	_, err := e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{
		TriggerId: "orphan", NewOwnerUserId: bob, Rebinding: &pb.TriggerRebinding{ProjectId: "bobs"},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	// Nor offer it to someone outside the org.
	_, err = e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{TriggerId: "orphan", NewOwnerUserId: olga}))
	assert.Equal(t, ReasonOffereeNotMember, reason(err))

	offer := e.offer(t, "orphan", bob)
	assert.Equal(t, core.TriggerOfferPending, offer.GetStatus())
	assert.Equal(t, e.now.Add(OfferTTL).Format(time.RFC3339), offer.GetExpiresAt())
	assert.Empty(t, e.triggers.calls(), "an offer re-owns nothing")
	row, _ := e.repo.GetTrigger(context.Background(), "orphan")
	assert.Equal(t, cora, row.UserID)

	// Visible only to its offeree (and admins): another member cannot accept
	// or even see it.
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(e.accept(e.as(carol), offer.GetId())))
	carolsOffers, err := e.svc.ListTriggerOffers(e.as(carol), connect.NewRequest(&pb.ListTriggerOffersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, carolsOffers.Msg.GetOffers())
	_, err = e.svc.RevokeTriggerOffer(e.as(carol), connect.NewRequest(&pb.RevokeTriggerOfferRequest{OfferId: offer.GetId()}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	bobsOffers, err := e.svc.ListTriggerOffers(e.as(bob), connect.NewRequest(&pb.ListTriggerOffersRequest{}))
	require.NoError(t, err)
	require.Len(t, bobsOffers.Msg.GetOffers(), 1)
	assert.Equal(t, []string{"api_token"}, bobsOffers.Msg.GetOffers()[0].GetParamNames())
	assert.Equal(t, "nightly (definition)", bobsOffers.Msg.GetOffers()[0].GetTrigger().GetName())

	// A machine or agent credential of the offeree cannot accept.
	machine := authz.WithMembership(auth.WithCredential(
		context.WithValue(context.Background(), auth.UserIDContextKey, bob), auth.CredentialAccessToken), e.members)
	assert.Equal(t, auth.ReasonPersonRequired, reason(e.accept(machine, offer.GetId())))

	require.NoError(t, e.accept(e.as(bob), offer.GetId()))
	assert.Equal(t, []adoption{{"orphan", bob, offer.GetId()}}, e.triggers.calls(), "the offeree's accept is what adopts it, as them")
	assert.Equal(t, core.TriggerOfferAccepted, e.repo.offer(offer.GetId()).Status)
	assert.Equal(t, ReasonOfferNotLive, reason(e.accept(e.as(bob), offer.GetId())), "an offer is accepted once")
}

// An admin adopting for themselves adopts at once, as themselves.
func TestAdminAdoptsToThemselves(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	_, err := e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{TriggerId: "orphan"}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "adopting means supplying a rebinding")

	resp, err := e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{
		TriggerId: "orphan", Rebinding: &pb.TriggerRebinding{ProjectId: "adas", DaemonId: "d"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "orphan", resp.Msg.GetTrigger().GetId())
	assert.Equal(t, []adoption{{"orphan", ada, ""}}, e.triggers.calls())
}

// A live member's automation is not an orphan: nobody takes it. A trigger
// of an org the caller is not in does not exist for them.
func TestAdoptTrigger_OnlyOrphans(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	_, err := e.svc.AdoptTrigger(e.as(ada), connect.NewRequest(&pb.AdoptTriggerRequest{
		TriggerId: "livs", Rebinding: &pb.TriggerRebinding{ProjectId: "adas"},
	}))
	assert.Equal(t, ReasonNotOrphaned, reason(err))

	_, err = e.svc.AdoptTrigger(e.as(olga), connect.NewRequest(&pb.AdoptTriggerRequest{TriggerId: "orphan"}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "an outsider cannot tell the trigger exists")
	_, err = e.svc.AdoptTrigger(e.as(bob), connect.NewRequest(&pb.AdoptTriggerRequest{TriggerId: "orphan"}))
	assert.Equal(t, orgtakeover.ReasonOrgAdminRequired, reason(err))
	assert.Empty(t, e.triggers.calls())
}

// An offer waits seven days for its offeree, then expires.
func TestOfferExpiresAfterSevenDays(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	offer := e.offer(t, "orphan", bob)

	e.now = e.now.Add(OfferTTL - time.Minute)
	listed, err := e.svc.ListTriggerOffers(e.as(bob), connect.NewRequest(&pb.ListTriggerOffersRequest{}))
	require.NoError(t, err)
	assert.Len(t, listed.Msg.GetOffers(), 1, "still live a minute before it expires")

	e.now = e.now.Add(time.Minute)
	err = e.accept(e.as(bob), offer.GetId())
	assert.Equal(t, ReasonOfferNotLive, reason(err))
	assert.Contains(t, err.Error(), "expired")
	assert.Equal(t, core.TriggerOfferExpired, e.repo.offer(offer.GetId()).Status, "a dead offer is settled as it is read")
	assert.Empty(t, e.triggers.calls())
}

// An offer is revoked when either party leaves the org, and when the trigger
// is no longer its departed creator's (they rejoined).
func TestOfferIsRevokedOnDeparture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(e *takeoverEnv)
		reason string
	}{
		{"the offeree leaves", func(e *takeoverEnv) { e.members.set(bob, false) }, core.TriggerOfferRevokedOffereeLeft},
		{"the admin who offered it leaves", func(e *takeoverEnv) { e.members.set(ada, false) }, core.TriggerOfferRevokedOffererLeft},
		{"the creator rejoins", func(e *takeoverEnv) { e.members.set(cora, true) }, core.TriggerOfferRevokedOwnerChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setupTakeover(t)
			offer := e.offer(t, "orphan", bob)
			tc.change(e)

			listed, err := e.svc.ListTriggerOffers(e.as(bob), connect.NewRequest(&pb.ListTriggerOffersRequest{}))
			require.NoError(t, err)
			assert.Empty(t, listed.Msg.GetOffers())
			got := e.repo.offer(offer.GetId())
			assert.Equal(t, core.TriggerOfferRevoked, got.Status)
			assert.Equal(t, tc.reason, got.StatusReason)
			assert.Equal(t, ReasonOfferNotLive, reason(e.accept(e.as(bob), offer.GetId())))
			assert.Empty(t, e.triggers.calls())
		})
	}
}

// A newer offer supersedes the last; the offeree declines and an admin
// withdraws.
func TestOffersSupersedeDeclineAndWithdraw(t *testing.T) {
	t.Parallel()
	e := setupTakeover(t)
	first := e.offer(t, "orphan", bob)
	second := e.offer(t, "orphan", carol)
	assert.Equal(t, core.TriggerOfferRevokedSuperseded, e.repo.offer(first.GetId()).StatusReason)
	assert.Equal(t, ReasonOfferNotLive, reason(e.accept(e.as(bob), first.GetId())))

	declined, err := e.svc.RevokeTriggerOffer(e.as(carol), connect.NewRequest(&pb.RevokeTriggerOfferRequest{OfferId: second.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, core.TriggerOfferRevokedDeclined, declined.Msg.GetOffer().GetStatusReason())

	third := e.offer(t, "orphan", bob)
	withdrawn, err := e.svc.RevokeTriggerOffer(e.as(ada), connect.NewRequest(&pb.RevokeTriggerOfferRequest{OfferId: third.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, core.TriggerOfferRevokedWithdrawn, withdrawn.Msg.GetOffer().GetStatusReason())
	assert.Equal(t, ReasonOfferNotLive, reason(e.accept(e.as(bob), third.GetId())))
	assert.Empty(t, e.triggers.calls())
}
