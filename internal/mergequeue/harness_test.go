// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
	"github.com/reliant-labs/reliant/pkg/config"
)

func TestMain(m *testing.M) {
	credsandbox.DispatchForTest()
	release, err := db.PrepareTestDatabases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare test databases: %v\n", err)
	}
	code := m.Run()
	if release != nil {
		release()
	}
	os.Exit(code)
}

var _ mergequeue.Store = (*db.Repo)(nil)

const (
	testOrg     = "org-1"
	testProject = "test-project"
	testUser    = "test-user"
)

// fixture is one enrolled repository on fakegithub, a real git workdir, a
// real Postgres and a scripted gate.
type fixture struct {
	t    *testing.T
	ctx  context.Context
	db   *db.Repo
	raw  *sql.DB
	g    *fakegithub.GitHub
	r    *fakegithub.Repo
	repo *core.PipelineRepo
	wd   mergequeue.Workdir
	gate *fakeGate
	land *fakeLander
	prs  map[string]*testPR
}

type testPR struct {
	branch, recordID, entryID, head string
	number                          int
}

func newFixture(t *testing.T, mode core.PipelineRepoMode) *fixture {
	t.Helper()
	ctx := t.Context()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	r.Commit("main", "base", map[string]string{"README.md": "widgets\n", "shared.txt": "one\n"})

	pr, err := repo.UpsertPipelineRepo(ctx, core.PipelineRepoEnrolment{
		OrgID: testOrg, ProjectID: testProject, Provider: "github", AccountKey: "inst-1",
		RepositoryID: "rid-1", Repository: "acme/widgets", CreatedByUserID: testUser,
	})
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if pr, err = repo.SetPipelineRepoMode(ctx, pr.ID, pr.Version, mode); err != nil {
		t.Fatalf("mode: %v", err)
	}
	wk, err := gitport.Init(ctx, filepath.Join(t.TempDir(), "work.git"), gitport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t: t, ctx: ctx, db: repo, raw: raw, g: g, r: r, repo: pr,
		wd:   mergequeue.Workdir{Git: wk, Remote: r.URL(), GitHub: g},
		gate: &fakeGate{g: g, r: r}, land: &fakeLander{}, prs: map[string]*testPR{},
	}
}

// setConfig records the repository's pipeline.yaml (as JSON).
func (f *fixture) setConfig(doc string) {
	f.t.Helper()
	r, err := f.db.GetPipelineRepo(f.ctx, f.repo.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.repo, err = f.db.SetPipelineRepoConfig(f.ctx, r.ID, r.Version, "cfg", []byte(doc)); err != nil {
		f.t.Fatalf("set config: %v", err)
	}
}

func (f *fixture) scheduler() *mergequeue.Scheduler {
	f.t.Helper()
	locks, err := dblock.New(dblock.Deps{Logger: slog.Default(), Config: &config.Config{}, Sessions: f.db.SessionPool()})
	if err != nil {
		f.t.Fatal(err)
	}
	s, err := mergequeue.New(mergequeue.Deps{
		Store: f.db, Leader: mergequeue.AdvisoryLeader{Locks: locks},
		Workdirs: fixedWorkdir{f.wd}, Gate: f.gate, Lander: f.land,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

type fixedWorkdir struct{ wd mergequeue.Workdir }

func (w fixedWorkdir) Open(context.Context, *core.PipelineRepo) (mergequeue.Workdir, error) {
	return w.wd, nil
}

// open opens a PR on branch, adding pr/<branch>.txt (plus files), with a
// record, a pipeline row and a pass verdict on its head, but no queue entry.
func (f *fixture) open(branch string, priority int, files map[string]string) *testPR {
	f.t.Helper()
	f.r.Branch(branch, "main")
	all := map[string]string{"pr/" + branch + ".txt": branch + "\n"}
	for k, v := range files {
		all[k] = v
	}
	head := f.r.Commit(branch, "work on "+branch, all)
	n := f.r.OpenPR(fakegithub.PROptions{Title: "Change " + branch, Head: branch})
	p := &testPR{branch: branch, recordID: "rec-" + branch, head: head, number: n}
	if _, err := f.raw.ExecContext(f.ctx, `INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, title, snapshot_at, observed_at) VALUES ($1, 'github', 'inst-1', 'rid-1', 'acme/widgets', $2, 'open', $3, $4, now(), now())`,
		p.recordID, n, head, "Change "+branch); err != nil {
		f.t.Fatalf("insert record: %v", err)
	}
	if _, _, err := f.db.EnsurePRPipeline(f.ctx, core.PRPipelineNew{
		RecordID: p.recordID, OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID,
		State: core.PRPipelineAwaitingReview, StateHeadSHA: head, Priority: priority, PrioritySource: core.PRPriorityDefault,
	}); err != nil {
		f.t.Fatalf("pr pipeline: %v", err)
	}
	f.pass(p, head)
	f.prs[branch] = p
	return p
}

func (f *fixture) pass(p *testPR, head string) {
	f.t.Helper()
	if _, err := f.db.InsertPRHeadVerdict(f.ctx, core.PRHeadVerdictRecord{
		OrgID: testOrg, ProjectID: testProject, RecordID: p.recordID, HeadSHA: head, Round: 1,
		Verdict: core.PRHeadVerdictPass, Rule: core.PRVerdictRuleNoModelLowRisk, RiskTier: "low", ReviewerPrincipal: "test",
	}); err != nil {
		f.t.Fatalf("verdict: %v", err)
	}
}

// queue opens a PR and enqueues its head.
func (f *fixture) queue(branch string, priority int, files map[string]string) *testPR {
	f.t.Helper()
	p := f.open(branch, priority, files)
	e, _, err := f.db.EnqueueQueueEntry(f.ctx, core.QueueEntryNew{
		OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID, RecordID: p.recordID, HeadSHA: p.head, Priority: priority,
	})
	if err != nil {
		f.t.Fatalf("enqueue: %v", err)
	}
	p.entryID = e.ID
	return p
}

func (f *fixture) tick(s *mergequeue.Scheduler) mergequeue.Report {
	f.t.Helper()
	rep, err := s.Tick(f.ctx, f.repo.ID)
	if err != nil {
		f.t.Fatalf("tick: %v", err)
	}
	return rep
}

func (f *fixture) batch(id string) *core.QueueBatch {
	f.t.Helper()
	b, err := f.db.GetQueueBatch(f.ctx, id)
	if err != nil || b == nil {
		f.t.Fatalf("batch %s: %v %v", id, b, err)
	}
	return b
}

func (f *fixture) entry(p *testPR) *core.QueueEntry {
	f.t.Helper()
	var e core.QueueEntry
	var batch, reason, landed sql.NullString
	if err := f.raw.QueryRowContext(f.ctx, `SELECT id, state, head_sha, batch_id, eject_reason, landed_sha FROM queue_entries WHERE id = $1`, p.entryID).
		Scan(&e.ID, &e.State, &e.HeadSHA, &batch, &reason, &landed); err != nil {
		f.t.Fatalf("entry %s: %v", p.entryID, err)
	}
	e.BatchID, e.EjectReason, e.LandedSHA = batch.String, core.QueueEjectReason(reason.String), landed.String
	return &e
}

func (f *fixture) pipeline(p *testPR) *core.PRPipeline {
	f.t.Helper()
	row, err := f.db.GetPRPipeline(f.ctx, p.recordID)
	if err != nil || row == nil {
		f.t.Fatalf("pr pipeline %s: %v %v", p.recordID, row, err)
	}
	return row
}

func (f *fixture) verdict(id string) mergequeue.Verdict {
	return mergequeue.ParseVerdict(f.batch(id).VerdictJSON)
}

func (f *fixture) queueState() core.PipelineQueueState {
	f.t.Helper()
	r, err := f.db.GetPipelineRepo(f.ctx, f.repo.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return r.QueueState
}

// wouldLand is a would-land set by branch.
func (f *fixture) branches(les []mergequeue.LandEntry) []string {
	var out []string
	for _, le := range les {
		for b, p := range f.prs {
			if p.entryID == le.EntryID {
				out = append(out, b)
			}
		}
	}
	return out
}

// fakeGate judges a candidate by the PRs in it (the pr/<branch>.txt files
// its tree holds), with a script.
type fakeGate struct {
	g *fakegithub.GitHub
	r *fakegithub.Repo

	mu sync.Mutex
	// judge decides a gate: the branches in the candidate, sorted, and how
	// many times that set was gated before. nil passes on sysbox.
	judge func(req mergequeue.GateRequest, branches []string, prior int) mergequeue.GateResult
	seen  map[string]int
	calls []gateCall
	// block, when set, holds every gate until it is closed; started is
	// signalled as each gate begins.
	block   chan struct{}
	started chan struct{}
}

type gateCall struct {
	req      mergequeue.GateRequest
	branches []string
}

func (g *fakeGate) Run(ctx context.Context, req mergequeue.GateRequest) (mergequeue.GateResult, error) {
	if g.started != nil {
		g.started <- struct{}{}
	}
	if g.block != nil {
		select {
		case <-g.block:
		case <-ctx.Done():
			return mergequeue.GateResult{}, ctx.Err()
		}
	}
	branches := g.branchesAt(ctx, req.CandidateSHA)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string]int{}
	}
	key := strings.Join(branches, ",")
	prior := g.seen[key]
	g.seen[key]++
	g.calls = append(g.calls, gateCall{req: req, branches: branches})
	if g.judge == nil {
		return pass(), nil
	}
	return g.judge(req, branches, prior), nil
}

func (g *fakeGate) branchesAt(ctx context.Context, sha string) []string {
	var out []string
	for _, b := range branchUniverse {
		if _, err := g.g.GetContents(ctx, g.r.ID(), "pr/"+b+".txt", sha); err == nil {
			out = append(out, b)
		}
	}
	sort.Strings(out)
	return out
}

// branchUniverse is every branch a test opens a PR on.
var branchUniverse = []string{"a", "b", "c", "d", "e", "f", "bad", "urgent", "flaky"}

func (g *fakeGate) gated() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out [][]string
	for _, c := range g.calls {
		out = append(out, c.branches)
	}
	return out
}

func (g *fakeGate) requests() []mergequeue.GateRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []mergequeue.GateRequest
	for _, c := range g.calls {
		out = append(out, c.req)
	}
	return out
}

func pass() mergequeue.GateResult {
	return mergequeue.GateResult{Class: mergequeue.GatePass, Isolation: "sysbox", Machine: "m1", Stages: []byte(`{"test":{"verdict":"pass"}}`)}
}

// fakeLander records the batches it is handed.
type fakeLander struct {
	mu    sync.Mutex
	lands []mergequeue.LandRequest
}

func (l *fakeLander) Land(_ context.Context, req mergequeue.LandRequest) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lands = append(l.lands, req)
	return nil
}
