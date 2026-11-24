package insights_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/insights"
	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// These tests run the service over a real database and the real usage writer
// (RecordLLMCallUsage, RecordToolCallUsage, which fold each call into the
// insights rollups), so what the team view, the drill-down and "My
// analytics" show is proven against what the worker actually writes.

// now is fixture data, not a clock: the service's injected time.
var now = time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)

// day is the UTC day every fixture call is made on.
var day = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

const (
	org   = "org-a"
	other = "org-b"
)

// directory is control-plane's membership answer: each org's members and
// their roles. It counts the asks, to prove a role is read fresh.
type directory struct {
	mu    sync.Mutex
	roles map[string]map[string]string // org -> user -> role
	asks  int
}

func (d *directory) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asks++
	role, ok := d.roles[orgID][userID]
	if !ok {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true, Role: role}, nil
}

func (d *directory) set(orgID, userID, role string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.roles[orgID] == nil {
		d.roles[orgID] = map[string]string{}
	}
	if role == "" {
		delete(d.roles[orgID], userID)
		return
	}
	d.roles[orgID][userID] = role
}

type env struct {
	t      *testing.T
	store  *storetest.Env
	ledger llmusage.Service
	dir    *directory
	// res is one Resolver for the whole test, as in a server: its cache is
	// what a fresh read must bypass.
	res *orgs.Resolver
	svc *insights.Service
	seq int
}

// The org's people: ada and owen administer it, the m* are members.
var members = []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := storetest.New(t)
	e := &env{t: t, store: s, ledger: llmusagetest.New(t, s.Repo), dir: &directory{roles: map[string]map[string]string{}}}
	e.res = orgs.New(e.dir)
	e.svc = e.service(s.Repo)
	e.dir.set(org, "ada", orgs.RoleAdmin)
	e.dir.set(org, "owen", orgs.RoleOwner)
	for _, m := range members {
		e.dir.set(org, m, orgs.RoleMember)
	}
	e.dir.set(other, "gus", orgs.RoleAdmin)
	for _, p := range []string{"proj-solo", "proj-shared"} {
		e.exec(`INSERT INTO projects (id, name, path, user_id, created_at, updated_at, last_active)
			VALUES ($1, $1, '/srv/' || $1, $2, $3, $3, $3)`, p, storetest.SeedUserID, day)
	}
	return e
}

func (e *env) service(members insights.MemberStore) *insights.Service {
	return insights.NewTestInsights(e.t, insights.WithInsightsDeps(insights.Deps{
		Team:    e.store.Repo,
		Members: members,
		Now:     func() time.Time { return now },
	}))
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	_, err := e.store.SQL.Exec(query, args...)
	require.NoError(e.t, err)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.store.SQL.QueryRow(query, args...).Scan(&n))
	return n
}

// as is userID acting through credential c.
func (e *env) as(userID string, c auth.Credential) context.Context {
	ctx := auth.WithUser(context.Background(), userID, "authenticated", "")
	ctx = auth.WithCredential(ctx, c)
	return authz.WithMembership(ctx, e.res)
}

// asPerson is userID signed in to the app.
func (e *env) asPerson(userID string) context.Context { return e.as(userID, auth.CredentialSession) }

// call is one model call's fixture fields; zero values take defaults.
type call struct {
	user, org, project, model, funding, basis string
	cost                                      int64
	latency                                   int64
}

func (e *env) model(c call) {
	e.t.Helper()
	e.seq++
	if c.org == "" {
		c.org = org
	}
	if c.model == "" {
		c.model = "m-large"
	}
	if c.funding == "" {
		c.funding = "org_wallet"
	}
	if c.latency == 0 {
		c.latency = 1000
	}
	rec := db.LLMCallUsage{
		ID: fmt.Sprintf("call-%d", e.seq), Attempt: 1, UserID: c.user, OrgID: c.org, ChatID: "chat-" + c.user,
		Provider: "reliant", Model: c.model, AuthMode: store.LLMAuthModeManaged, Purpose: store.LLMPurposeChat,
		InputTokens: 100, OutputTokens: 10, Outcome: store.LLMOutcomeSuccess, LatencyMs: c.latency,
		Funding: c.funding, ProjectID: c.project, WorkflowName: "agent", CreatedAt: day,
	}
	if c.basis != "" {
		rec.CostBasis = c.basis
		if c.basis != store.LLMCostBasisUnpriced {
			cost := c.cost
			rec.CostUSDNanos = &cost
		}
	}
	_, err := e.ledger.RecordLLMCallUsage(context.Background(), rec)
	require.NoError(e.t, err)
}

func (e *env) tool(user, project, name string) {
	e.t.Helper()
	e.seq++
	_, err := e.ledger.RecordToolCallUsage(context.Background(), store.ToolCallUsageRecord{
		ID: fmt.Sprintf("tool-%d", e.seq), Attempt: 1, UserID: user, OrgID: org, ChatID: "chat-" + user, ProjectID: project,
		ToolKind: store.ToolKindBuiltin, ToolName: name, Location: "daemon", Outcome: store.ToolOutcomeSuccess,
		DurationMs: 250, CreatedAt: day,
	})
	require.NoError(e.t, err)
}

func (e *env) team(ctx context.Context, groupBy pb.InsightsGroupBy) (*pb.GetTeamInsightsResponse, error) {
	resp, err := e.svc.GetTeamInsights(ctx, connect.NewRequest(&pb.GetTeamInsightsRequest{
		OrgId: org, StartDate: "2026-11-01", EndDate: "2026-11-30", GroupBy: groupBy}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *env) drill(ctx context.Context, subject string) (*pb.GetMemberInsightsResponse, error) {
	resp, err := e.svc.GetMemberInsights(ctx, connect.NewRequest(&pb.GetMemberInsightsRequest{
		OrgId: org, UserId: subject, StartDate: "2026-11-01", EndDate: "2026-11-30"}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *env) mine(ctx context.Context, groupBy pb.InsightsGroupBy) (*pb.GetMyInsightsResponse, error) {
	resp, err := e.svc.GetMyInsights(ctx, connect.NewRequest(&pb.GetMyInsightsRequest{
		OrgId: org, StartDate: "2026-11-01", EndDate: "2026-11-30", GroupBy: groupBy}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *env) audit(ctx context.Context) (*pb.ListDrillDownAuditResponse, error) {
	resp, err := e.svc.ListDrillDownAudit(ctx, connect.NewRequest(&pb.ListDrillDownAuditRequest{OrgId: org}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *env) auditRows() int {
	return e.count(`SELECT count(*) FROM insights_drilldowns`)
}

func assertDenied(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, connect.CodeOf(err), "%v", err)
	if reason != "" {
		got, _ := svcerr.ReasonOf(err)
		assert.Equal(t, reason, got, "%v", err)
	}
}

func modelKeys(sec *pb.InsightsModelSection) []string {
	out := []string{}
	for _, b := range sec.GetBuckets() {
		out = append(out, b.GetKey())
	}
	return out
}

// stringsIn is every string value anywhere in m, recursively.
func stringsIn(m protoreflect.Message) []string {
	var out []string
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				if fd.Kind() == protoreflect.MessageKind {
					out = append(out, stringsIn(l.Get(i).Message())...)
				} else if fd.Kind() == protoreflect.StringKind {
					out = append(out, l.Get(i).String())
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			out = append(out, stringsIn(v.Message())...)
		case fd.Kind() == protoreflect.StringKind:
			out = append(out, v.String())
		}
		return true
	})
	return out
}

// refusingMembers is a MemberStore any use of which fails the test: the team
// view must never reach a member's rows.
type refusingMembers struct{ t *testing.T }

func (r refusingMembers) ListInsightsMemberModelCalls(context.Context, string, string, time.Time, time.Time, db.InsightsDim) ([]db.InsightsModelRow, error) {
	r.t.Error("the team view read a member's model calls")
	return nil, errors.New("refused")
}

func (r refusingMembers) ListInsightsMemberToolCalls(context.Context, string, string, time.Time, time.Time, db.InsightsDim) ([]db.InsightsToolRow, error) {
	r.t.Error("the team view read a member's tool calls")
	return nil, errors.New("refused")
}

func (r refusingMembers) RecordInsightsDrilldown(context.Context, string, string, string, time.Time, time.Time, time.Time) (string, error) {
	r.t.Error("the team view recorded a drill-down")
	return "", errors.New("refused")
}

func (r refusingMembers) ListInsightsDrilldowns(context.Context, string, string, *db.InsightsDrilldownCursor, int) ([]*db.InsightsDrilldown, error) {
	r.t.Error("the team view read the drill-down audit")
	return nil, errors.New("refused")
}

// The team view never returns a person: under every grouping, no string in
// the response is a member's id, and it never touches a member store.
func TestTeamInsightsNeverReturnsAPerson(t *testing.T) {
	e := newEnv(t)
	e.svc = e.service(refusingMembers{t: t})
	people := append([]string{"ada", "owen"}, members...)
	for i, m := range people {
		e.model(call{user: m, project: "proj-shared", model: "m-large"})
		e.model(call{user: m, project: "proj-solo", model: fmt.Sprintf("m-%d", i%2)})
		e.tool(m, "proj-shared", "shell")
	}
	for g := range pb.InsightsGroupBy_name {
		groupBy := pb.InsightsGroupBy(g)
		t.Run(groupBy.String(), func(t *testing.T) {
			resp, err := e.team(e.asPerson("m1"), groupBy)
			require.NoError(t, err)
			for _, s := range stringsIn(resp.ProtoReflect()) {
				assert.NotContains(t, people, s, "the team view returned a person's id")
			}
		})
	}
	// And the only types it can return have no field for one.
	for _, m := range []proto.Message{&pb.GetTeamInsightsResponse{}, &pb.InsightsModelSection{}, &pb.InsightsToolSection{},
		&pb.InsightsModelBucket{}, &pb.InsightsToolBucket{}, &pb.InsightsModelStats{}, &pb.InsightsToolStats{}, &pb.InsightsCost{}} {
		fields := m.ProtoReflect().Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			assert.NotContains(t, string(fields.Get(i).Name()), "user", "%s has a field naming a user", m.ProtoReflect().Descriptor().FullName())
		}
	}
}

// A bucket fewer than five members contributed to is merged into other; and
// while other has fewer than five contributors, the smallest shown bucket
// joins it, so other never singles anyone out either.
func TestTeamInsightsFloorMergesSmallBuckets(t *testing.T) {
	e := newEnv(t)
	// m-big: m1..m6. m-mid: m1..m5. m-seven: m7 alone. m-eight: m8 alone.
	for _, m := range members[:6] {
		e.model(call{user: m, model: "m-big"})
	}
	for _, m := range members[:5] {
		e.model(call{user: m, model: "m-mid"})
		e.model(call{user: m, model: "m-mid"})
	}
	e.model(call{user: "m7", model: "m-seven"})
	e.model(call{user: "m8", model: "m-eight"})

	resp, err := e.team(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL)
	require.NoError(t, err)
	sec := resp.GetModelCalls()
	// m-seven and m-eight are merged, but together they are two people, so
	// m-mid (the shown bucket with the fewest contributors) joins them.
	assert.Equal(t, []string{"m-big"}, modelKeys(sec))
	assert.Equal(t, int32(3), sec.GetOtherGroups())
	assert.Equal(t, int64(10+1+1), sec.GetOther().GetCalls())
	assert.Equal(t, int64(6+12), sec.GetTotals().GetCalls())
	assert.Equal(t, int32(insights.MinContributors), resp.GetMinContributors())

	// When the small buckets together reach five people, they are merged and
	// nothing else is.
	for _, m := range []string{"m6", "m1", "m2"} {
		e.model(call{user: m, model: "m-seven"})
	}
	resp, err = e.team(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL)
	require.NoError(t, err)
	assert.Equal(t, []string{"m-mid", "m-big"}, modelKeys(resp.GetModelCalls()))
	assert.Equal(t, int32(2), resp.GetModelCalls().GetOtherGroups())
}

// The floor counts contributors per project bucket: a project belongs to one
// member, so its bucket would be that member's numbers however many calls it
// holds. Grouping by any other dimension counts contributors to that
// dimension's buckets the same way.
func TestTeamInsightsFloorCountsContributorsPerProject(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 50; i++ {
		e.model(call{user: "m1", project: "proj-solo"})
	}
	for _, m := range members[:5] {
		e.model(call{user: m, project: "proj-shared"})
		e.tool(m, "proj-shared", "shell")
	}
	for _, m := range members[5:] {
		e.model(call{user: m}) // no project
	}

	resp, err := e.team(e.asPerson("m2"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PROJECT)
	require.NoError(t, err)
	// proj-solo (1 contributor) and no-project (3) are merged: four people.
	// That is under five, so proj-shared joins them, and nothing is shown.
	assert.Empty(t, resp.GetModelCalls().GetBuckets())
	assert.Equal(t, int64(58), resp.GetModelCalls().GetOther().GetCalls())
	assert.Equal(t, int64(58), resp.GetModelCalls().GetTotals().GetCalls())
	// Tools: only proj-shared, five people: shown.
	require.Len(t, resp.GetToolCalls().GetBuckets(), 1)
	assert.Equal(t, "proj-shared", resp.GetToolCalls().GetBuckets()[0].GetKey())
	assert.Nil(t, resp.GetToolCalls().Other)

	// One more member in the no-project bucket makes the merged groups five
	// people (m1, and m3, m6, m7, m8): proj-shared is then shown.
	e.model(call{user: "m3"})
	resp, err = e.team(e.asPerson("m2"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PROJECT)
	require.NoError(t, err)
	assert.Equal(t, []string{"proj-shared"}, modelKeys(resp.GetModelCalls()))
	assert.Equal(t, int32(2), resp.GetModelCalls().GetOtherGroups())
	assert.NotContains(t, modelKeys(resp.GetModelCalls()), "proj-solo")
}

// A small org sees only "other": its totals, no breakdown.
func TestTeamInsightsSmallOrgSeesOnlyOther(t *testing.T) {
	e := newEnv(t)
	for _, m := range members[:3] {
		e.model(call{user: m})
	}
	resp, err := e.team(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	require.NoError(t, err)
	assert.Empty(t, resp.GetModelCalls().GetBuckets())
	assert.Equal(t, int64(3), resp.GetModelCalls().GetOther().GetCalls())
	assert.Equal(t, int64(3), resp.GetModelCalls().GetTotals().GetCalls())
}

// Any live member reads the team view; anyone else is refused, and a removed
// member is refused at once (the role is read fresh, not from the cache).
func TestTeamInsightsNeedsALiveMember(t *testing.T) {
	e := newEnv(t)
	e.model(call{user: "m1"})

	_, err := e.team(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	require.NoError(t, err)
	_, err = e.team(e.as("m1", auth.CredentialAccessToken), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	require.NoError(t, err, "a member's own CLI login may read the team view")

	_, err = e.team(e.asPerson("gus"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)

	e.dir.set(org, "m1", "")
	_, err = e.team(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)
}

// A drill-down is for an org admin, writes an audit row before it answers,
// and is refused for a machine credential and for a non-admin (with no row).
func TestMemberInsightsAdminOnlyAndAudited(t *testing.T) {
	e := newEnv(t)
	e.model(call{user: "m1"})
	e.model(call{user: "m1"})
	e.model(call{user: "m2"})

	got, err := e.drill(e.asPerson("ada"), "m1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.GetModelCalls().GetTotals().GetCalls())
	assert.Nil(t, got.GetModelCalls().Other, "a drill-down has no floor")
	require.NotEmpty(t, got.GetAuditId())
	var actor, subject, from, to string
	require.NoError(t, e.store.SQL.QueryRow(`SELECT actor_user_id, subject_user_id, start_date::text, end_date::text
		FROM insights_drilldowns WHERE id = $1 AND org_id = $2`, got.GetAuditId(), org).Scan(&actor, &subject, &from, &to))
	assert.Equal(t, []string{"ada", "m1", "2026-11-01", "2026-11-30"}, []string{actor, subject, from, to})

	_, err = e.drill(e.asPerson("owen"), "m2")
	require.NoError(t, err, "an owner may drill down")
	assert.Equal(t, 2, e.auditRows())

	// Refused, and nothing recorded: a machine credential (even an admin's),
	// a member, a non-member, another org's admin.
	for name, ctx := range map[string]context.Context{
		"admin's access token": e.as("ada", auth.CredentialAccessToken),
		"admin's OAuth client": e.as("ada", auth.CredentialOAuthClient),
	} {
		_, err := e.drill(ctx, "m1")
		assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonHumanOnly)
		assert.Equal(t, 2, e.auditRows(), name)
	}
	for _, who := range []string{"m3", "gus", "stranger"} {
		_, err := e.drill(e.asPerson(who), "m1")
		assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired)
		assert.Equal(t, 2, e.auditRows(), who)
	}

	// The role is read fresh: an admin demoted a moment ago is refused.
	asks := e.dir.asks
	e.dir.set(org, "ada", orgs.RoleMember)
	_, err = e.drill(e.asPerson("ada"), "m1")
	assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired)
	assert.Greater(t, e.dir.asks, asks)
	assert.Equal(t, 2, e.auditRows())
}

// failingAudit records nothing: the drill-down must then show nothing.
type failingAudit struct {
	insights.MemberStore
	t *testing.T
}

func (f failingAudit) RecordInsightsDrilldown(context.Context, string, string, string, time.Time, time.Time, time.Time) (string, error) {
	return "", errors.New("audit table unavailable")
}

func (f failingAudit) ListInsightsMemberModelCalls(context.Context, string, string, time.Time, time.Time, db.InsightsDim) ([]db.InsightsModelRow, error) {
	f.t.Error("a drill-down read a member's numbers without recording it")
	return nil, errors.New("refused")
}

func TestMemberInsightsUnrecordedIsUnanswered(t *testing.T) {
	e := newEnv(t)
	e.model(call{user: "m1"})
	e.svc = e.service(failingAudit{MemberStore: e.store.Repo, t: t})
	_, err := e.drill(e.asPerson("ada"), "m1")
	assertDenied(t, err, connect.CodeUnavailable, "")
}

// A member reads their own row, and it is what an admin's drill-down shows
// about them; no one else's calls are in it.
func TestMyInsightsReadsOwnRow(t *testing.T) {
	e := newEnv(t)
	e.model(call{user: "m1", model: "m-large"})
	e.model(call{user: "m1", model: "m-small"})
	e.model(call{user: "m2", model: "m-large"})
	e.tool("m1", "", "shell")
	e.tool("m2", "", "read_file")
	e.model(call{user: "m1", org: other}) // another org's call

	mine, err := e.mine(e.as("m1", auth.CredentialAccessToken), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL)
	require.NoError(t, err)
	assert.Equal(t, int64(2), mine.GetModelCalls().GetTotals().GetCalls())
	assert.ElementsMatch(t, []string{"m-large", "m-small"}, modelKeys(mine.GetModelCalls()))
	assert.Empty(t, mine.GetToolCalls().GetBuckets(), "model grouping has no tool buckets")

	byDay, err := e.mine(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	require.NoError(t, err)
	assert.Equal(t, int64(1), byDay.GetToolCalls().GetTotals().GetCalls())
	require.Len(t, byDay.GetModelCalls().GetBuckets(), 1)
	assert.Equal(t, "2026-11-23", byDay.GetModelCalls().GetBuckets()[0].GetKey())

	drill, err := e.drill(e.asPerson("ada"), "m1")
	require.NoError(t, err)
	assert.True(t, proto.Equal(drill.GetModelCalls(), byDay.GetModelCalls()), "the drill-down shows what the member sees")
	assert.True(t, proto.Equal(drill.GetToolCalls(), byDay.GetToolCalls()))

	_, err = e.mine(e.asPerson("gus"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)
}

// A member sees the drill-downs into themselves; an admin sees every one.
func TestDrillDownAuditVisibility(t *testing.T) {
	e := newEnv(t)
	_, err := e.drill(e.asPerson("ada"), "m1")
	require.NoError(t, err)
	_, err = e.drill(e.asPerson("owen"), "m2")
	require.NoError(t, err)

	m1, err := e.audit(e.asPerson("m1"))
	require.NoError(t, err)
	assert.Equal(t, pb.ListDrillDownAuditScope_LIST_DRILL_DOWN_AUDIT_SCOPE_SELF, m1.GetScope())
	require.Len(t, m1.GetEvents(), 1)
	assert.Equal(t, "ada", m1.GetEvents()[0].GetActorUserId())
	assert.Equal(t, "m1", m1.GetEvents()[0].GetSubjectUserId())

	m3, err := e.audit(e.asPerson("m3"))
	require.NoError(t, err)
	assert.Empty(t, m3.GetEvents())

	all, err := e.audit(e.asPerson("ada"))
	require.NoError(t, err)
	assert.Equal(t, pb.ListDrillDownAuditScope_LIST_DRILL_DOWN_AUDIT_SCOPE_ORG, all.GetScope())
	assert.Len(t, all.GetEvents(), 2)

	// An admin's CLI login reads only what any member reads.
	cli, err := e.audit(e.as("ada", auth.CredentialAccessToken))
	require.NoError(t, err)
	assert.Equal(t, pb.ListDrillDownAuditScope_LIST_DRILL_DOWN_AUDIT_SCOPE_SELF, cli.GetScope())
	assert.Empty(t, cli.GetEvents())

	_, err = e.audit(e.asPerson("gus"))
	assertDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)
}

func TestDrillDownAuditPages(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		_, err := e.drill(e.asPerson("ada"), members[i])
		require.NoError(t, err)
	}
	var seen []string
	token := ""
	for {
		resp, err := e.svc.ListDrillDownAudit(e.asPerson("ada"), connect.NewRequest(&pb.ListDrillDownAuditRequest{
			OrgId: org, PageSize: 2, PageToken: token}))
		require.NoError(t, err)
		for _, ev := range resp.Msg.GetEvents() {
			seen = append(seen, ev.GetSubjectUserId())
		}
		if token = resp.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	assert.ElementsMatch(t, members[:5], seen)
	_, err := e.svc.ListDrillDownAudit(e.asPerson("ada"), connect.NewRequest(&pb.ListDrillDownAuditRequest{OrgId: org, PageToken: "not-a-token"}))
	assertDenied(t, err, connect.CodeInvalidArgument, "")
}

// Personally funded calls count as calls, with their cost blank, never zero;
// an org-funded call the gateway priced at zero is a zero, not a blank.
func TestPersonallyFundedCostIsBlankNotZero(t *testing.T) {
	e := newEnv(t)
	// m1's own subscription, priced by the gateway: the member table keeps
	// its cost, and Insights must not show it.
	e.model(call{user: "m1", model: "m-personal", funding: "personal", basis: store.LLMCostBasisReported, cost: 4_000_000})
	e.model(call{user: "m1", model: "m-personal", funding: "local", basis: store.LLMCostBasisUnpriced})
	e.model(call{user: "m1", model: "m-free", funding: "org_wallet", basis: store.LLMCostBasisReported, cost: 0})
	e.model(call{user: "m1", model: "m-paid", funding: "org_key", basis: store.LLMCostBasisEstimated, cost: 7_000})

	mine, err := e.mine(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL)
	require.NoError(t, err)
	byKey := map[string]*pb.InsightsCost{}
	for _, b := range mine.GetModelCalls().GetBuckets() {
		byKey[b.GetKey()] = b.GetStats().GetCost()
	}
	personal := byKey["m-personal"]
	require.NotNil(t, personal)
	assert.Nil(t, personal.ReportedUsdNanos, "a personally funded cost is blank")
	assert.Nil(t, personal.EstimatedUsdNanos)
	assert.Equal(t, int64(2), personal.GetPersonallyFundedCalls())
	assert.Zero(t, personal.GetOrgFundedCalls())
	assert.Zero(t, personal.GetUnpricedCalls(), "a personal call is not an org-funded unpriced call")

	free := byKey["m-free"]
	require.NotNil(t, free.ReportedUsdNanos, "an org-funded call priced at zero shows zero")
	assert.Equal(t, int64(0), free.GetReportedUsdNanos())
	assert.Nil(t, free.EstimatedUsdNanos)

	paid := byKey["m-paid"]
	assert.Nil(t, paid.ReportedUsdNanos, "an estimate is never a reported cost")
	assert.Equal(t, int64(7_000), paid.GetEstimatedUsdNanos())

	totals := mine.GetModelCalls().GetTotals().GetCost()
	assert.Equal(t, int64(0), totals.GetReportedUsdNanos())
	assert.Equal(t, int64(7_000), totals.GetEstimatedUsdNanos())
	assert.Equal(t, int64(4), mine.GetModelCalls().GetTotals().GetCalls(), "personally funded calls count as calls")

	// The admin's drill-down shows the same blank.
	drill, err := e.drill(e.asPerson("ada"), "m1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), drill.GetModelCalls().GetTotals().GetCost().GetReportedUsdNanos())

	// And the team view: a bucket of personal calls only has a blank cost.
	// Five members use each model, so no bucket is merged.
	for _, m := range members[1:5] {
		e.model(call{user: m, model: "m-personal", funding: "personal", basis: store.LLMCostBasisReported, cost: 1_000})
		e.model(call{user: m, model: "m-free", funding: "org_wallet", basis: store.LLMCostBasisReported, cost: 0})
		e.model(call{user: m, model: "m-paid", funding: "org_key", basis: store.LLMCostBasisEstimated, cost: 7_000})
	}
	team, err := e.team(e.asPerson("m2"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"m-personal", "m-free", "m-paid"}, modelKeys(team.GetModelCalls()))
	teamCost := map[string]*pb.InsightsCost{}
	for _, b := range team.GetModelCalls().GetBuckets() {
		teamCost[b.GetKey()] = b.GetStats().GetCost()
	}
	assert.Nil(t, teamCost["m-personal"].ReportedUsdNanos)
	assert.Equal(t, int64(6), teamCost["m-personal"].GetPersonallyFundedCalls())
	require.NotNil(t, teamCost["m-free"].ReportedUsdNanos)
	assert.Equal(t, int64(0), teamCost["m-free"].GetReportedUsdNanos())
	assert.Equal(t, int64(35_000), teamCost["m-paid"].GetEstimatedUsdNanos())
}

// Latency percentiles are read off the rollup histograms.
func TestMyInsightsLatency(t *testing.T) {
	e := newEnv(t)
	for _, ms := range []int64{100, 100, 100, 100, 100, 100, 100, 100, 100, 10_000} {
		e.model(call{user: "m1", latency: ms})
	}
	mine, err := e.mine(e.asPerson("m1"), pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY)
	require.NoError(t, err)
	l := mine.GetModelCalls().GetTotals().GetLatency()
	assert.Equal(t, int64(10), l.GetCount())
	assert.InDelta(t, 1090, l.GetAvgMs(), 0.001)
	// 100 ms is in [2^6.5, 2^7) ≈ [90.5, 128); 10 s in [2^13, 2^13.5).
	assert.GreaterOrEqual(t, l.GetP50Ms(), 90.5)
	assert.Less(t, l.GetP50Ms(), 128.0)
	assert.GreaterOrEqual(t, l.GetP95Ms(), 8192.0)
	assert.Less(t, l.GetP95Ms(), 11586.0)
}

func TestInsightsRejectsBadRanges(t *testing.T) {
	e := newEnv(t)
	for name, req := range map[string]*pb.GetMyInsightsRequest{
		"reversed":  {OrgId: org, StartDate: "2026-11-30", EndDate: "2026-11-01"},
		"too long":  {OrgId: org, StartDate: "2025-01-01", EndDate: "2026-11-01"},
		"not dates": {OrgId: org, StartDate: "yesterday", EndDate: "2026-11-01"},
	} {
		_, err := e.svc.GetMyInsights(e.asPerson("m1"), connect.NewRequest(req))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), name)
	}
}
