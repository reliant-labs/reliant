package practions_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/grants/grantstest"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// grantsHarness is a harness whose actions check capability grants, with
// an activation on a pool and a member machine its runs execute on.
type grantsHarness struct {
	*harness
	grants    *grants.Service
	f         grantstest.Fixture
	principal serveraction.Principal
}

func newGrantsHarness(t *testing.T, mode core.PipelineRepoMode) *grantsHarness {
	t.Helper()
	h := newHarness(t, mode)
	g, err := grants.New(grants.Deps{Store: store.NewPipelineGrantExtStore(h.env.ORM), Cards: h.env.Repo})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := practions.New(practions.Deps{Store: h.env.Repo, GitHub: h.gh, Inbox: h.inbox, Grants: g, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	f := grantstest.Seed(t, h.env.Repo, storetest.SeedUserID, testOrg, storetest.SeedProjectID)
	return &grantsHarness{harness: h, grants: g, f: f, principal: serveraction.Principal{
		UserID: storetest.SeedUserID, OrgID: testOrg, ProjectID: storetest.SeedProjectID, TriggerID: f.TriggerID, DaemonID: f.DaemonID,
	}}
}

// grant approves a card granting caps to the fixture's activation.
func (g *grantsHarness) grant(caps ...core.PipelineCapability) {
	g.t.Helper()
	meta, _ := json.Marshal(core.CapabilityGrantRequest{Kind: core.ApprovalKindPipelineCapabilityGrant,
		TriggerID: g.f.TriggerID, MachinePoolID: g.f.PoolID, Capabilities: caps})
	m := string(meta)
	card := &core.Approval{ID: uuid.NewString(), ChatID: g.f.ChatID, ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		EntityID: uuid.NewString(), Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), Title: "grant", Metadata: &m, CreatedAt: g.now}
	if err := g.env.Repo.CreateApproval(g.ctx, card); err != nil {
		g.t.Fatal(err)
	}
	if _, err := g.grants.GrantApproved(g.ctx, g.f.UserID, card); err != nil {
		g.t.Fatal(err)
	}
}

func (g *grantsHarness) call(c call) call {
	p := g.principal
	c.principal = &p
	return c
}

func (g *grantsHarness) verdicts() []*core.PRHeadVerdictRecord {
	g.t.Helper()
	vs, err := g.env.Repo.ListPRHeadVerdicts(g.ctx, g.record)
	if err != nil {
		g.t.Fatal(err)
	}
	return vs
}

// In mode on a pass counts only from a run whose activation holds approve
// on a pool its machine is a member of: without it nothing is written, and
// once the owner grants it the pass is recorded with its activation and
// machine.
func TestVerdictPassNeedsTheApproveGrant(t *testing.T) {
	t.Parallel()
	g := newGrantsHarness(t, core.PipelineRepoModeOn)
	if next := g.must(serveraction.PRNext, g.call(call{node: "decide"})); next["next"] != practions.NextReview {
		t.Fatalf("to the reviewer: %v", next)
	}
	_, err := g.do(serveraction.PRVerdict, g.call(verdictCall(review("pass", "session-r1"))))
	refused(t, err, serveraction.CodePermissionDenied)
	if vs := g.verdicts(); len(vs) != 0 {
		t.Fatalf("a refused pass writes nothing: %+v", vs)
	}
	if r := g.row(); r.State != core.PRPipelineReviewing {
		t.Fatalf("still reviewing: %+v", r)
	}

	// A land grant is not an approve grant.
	g.grant(grants.Land)
	_, err = g.do(serveraction.PRVerdict, g.call(verdictCall(review("pass", "session-r1"))))
	refused(t, err, serveraction.CodePermissionDenied)

	// A run on a machine outside the pool holds nothing, granted or not.
	g.grant(grants.Approve)
	outside := g.call(verdictCall(review("pass", "session-r1")))
	outside.principal.DaemonID = grantstest.Machine(t, g.env.Repo, g.f.UserID, map[string]string{"pool": "elsewhere"})
	_, err = g.do(serveraction.PRVerdict, outside)
	refused(t, err, serveraction.CodePermissionDenied)

	out := g.must(serveraction.PRVerdict, g.call(verdictCall(review("pass", "session-r1"))))
	if out["verdict"] != "pass" || out["next"] != practions.NextEnqueue {
		t.Fatalf("a granted pass enqueues: %v", out)
	}
	vs := g.verdicts()
	if len(vs) != 1 || vs[0].TriggerID != g.f.TriggerID || vs[0].DaemonID != g.f.DaemonID {
		t.Fatalf("the verdict names its activation and machine: %+v", vs)
	}
}

// A changes verdict needs no grant: it gives no power.
func TestVerdictChangesNeedsNoGrant(t *testing.T) {
	t.Parallel()
	g := newGrantsHarness(t, core.PipelineRepoModeOn)
	g.must(serveraction.PRNext, g.call(call{node: "decide"}))
	out := g.must(serveraction.PRVerdict, g.call(verdictCall(review("changes", "session-r1",
		finding("f1", "internal/app/service.go", "nil deref")))))
	if out["verdict"] != "changes" {
		t.Fatalf("changes recorded: %v", out)
	}
	if vs := g.verdicts(); len(vs) != 1 || vs[0].TriggerID != g.f.TriggerID {
		t.Fatalf("recorded with its activation: %+v", vs)
	}
}

// Shadow mode writes nothing to GitHub, so a pass without the grant is
// recorded (and logged as one that mode on would refuse).
func TestVerdictPassInShadowIsRecordedWithoutAGrant(t *testing.T) {
	t.Parallel()
	g := newGrantsHarness(t, core.PipelineRepoModeShadow)
	g.must(serveraction.PRNext, g.call(call{node: "decide"}))
	out := g.must(serveraction.PRVerdict, g.call(verdictCall(review("pass", "session-r1"))))
	if out["verdict"] != "pass" {
		t.Fatalf("shadow records the pass: %v", out)
	}
	if vs := g.verdicts(); len(vs) != 1 || vs[0].DaemonID != g.f.DaemonID {
		t.Fatalf("recorded: %+v", vs)
	}
}

// With no authorizer at all, mode on refuses every pass: fail closed.
func TestVerdictPassWithNoAuthorizerIsRefusedInModeOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOn)
	h.must(serveraction.PRNext, call{node: "decide"})
	_, err := h.do(serveraction.PRVerdict, verdictCall(review("pass", "session-r1")))
	refused(t, err, serveraction.CodePermissionDenied)
}

// pr.next's own pass of a low-risk head approves as a model's does, so in
// mode on it too needs the approve grant: refused and unwritten without
// it, and recorded with its activation and machine once granted.
func TestStructuralPassNeedsTheApproveGrantInModeOn(t *testing.T) {
	t.Parallel()
	g := newGrantsHarness(t, core.PipelineRepoModeOn)
	g.gh.set(g.number, "head-1", "docs/guide.md@b1")
	_, err := g.do(serveraction.PRNext, g.call(call{node: "decide"}))
	refused(t, err, serveraction.CodePermissionDenied)
	if vs := g.verdicts(); len(vs) != 0 {
		t.Fatalf("a refused structural pass writes nothing: %+v", vs)
	}

	g.grant(grants.Approve)
	if next := g.must(serveraction.PRNext, g.call(call{node: "decide"})); next["next"] != practions.NextEnqueue {
		t.Fatalf("a granted structural pass enqueues: %v", next)
	}
	vs := g.verdicts()
	if len(vs) != 1 || vs[0].Rule != core.PRVerdictRuleNoModelLowRisk || vs[0].TriggerID != g.f.TriggerID || vs[0].DaemonID != g.f.DaemonID {
		t.Fatalf("the structural verdict names its activation and machine: %+v", vs)
	}
}

// In shadow the structural pass is recorded without a grant, with its
// activation and machine.
func TestStructuralPassInShadowIsRecordedWithoutAGrant(t *testing.T) {
	t.Parallel()
	g := newGrantsHarness(t, core.PipelineRepoModeShadow)
	g.gh.set(g.number, "head-1", "docs/guide.md@b1")
	if next := g.must(serveraction.PRNext, g.call(call{node: "decide"})); next["next"] != practions.NextEnqueue {
		t.Fatalf("shadow enqueues: %v", next)
	}
	if vs := g.verdicts(); len(vs) != 1 || vs[0].TriggerID != g.f.TriggerID {
		t.Fatalf("recorded with its activation: %+v", vs)
	}
}
