// Copyright (c) 2025 Reliant Labs
package lander_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
)

func TestMain(m *testing.M) {
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

const (
	testOrg     = "org-1"
	testProject = "test-project"
	testUser    = "test-user"
)

// store is *db.Repo plus the scheduler's PR record read (M7 adds
// GetQueuePRRecord to *db.Repo; until it merges the test reads the row
// itself).
type store struct {
	*db.Repo
	raw *sql.DB
}

func (s store) GetQueuePRRecord(ctx context.Context, id string) (*core.PRRecord, error) {
	var r core.PRRecord
	var headRef sql.NullString
	err := s.raw.QueryRowContext(ctx, `SELECT id, repository, number, state, head_sha, head_ref, title FROM pr_records WHERE id = $1`, id).
		Scan(&r.ID, &r.Repository, &r.Number, &r.State, &r.HeadSHA, &headRef, &r.Title)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	r.HeadRef = headRef.String
	return &r, err
}

var _ lander.Store = store{}

// creds hands out the fake GitHub as the identity a test picks.
type creds struct {
	gh ghport.Client
	id lander.Identity
}

func (c creds) Client(context.Context, *core.PipelineRepo) (ghport.Client, lander.Identity, error) {
	return c.gh, c.id, nil
}

// verifier answers a scripted verify verdict and records what it gated.
type verifier struct {
	mu    sync.Mutex
	class lander.VerifyClass
	got   []string
}

func (v *verifier) Verify(_ context.Context, req lander.VerifyRequest) (lander.VerifyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.got = append(v.got, req.SHA)
	return lander.VerifyResult{Class: v.class, Isolation: core.QueueIsolationSysbox, Machine: "gate-1"}, nil
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	st    store
	g     *fakegithub.GitHub
	r     *fakegithub.Repo
	repo  *core.PipelineRepo
	id    lander.Identity
	ver   lander.Verifier
	waits []time.Duration
	prs   []*testPR
	base  string
}

type testPR struct {
	branch, recordID, entryID, head, file string
	number                                int
}

func newFixture(t *testing.T, mode core.PipelineRepoMode) *fixture {
	t.Helper()
	ctx := t.Context()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	base := r.Commit("main", "base", map[string]string{"README.md": "widgets\n"})
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
	return &fixture{t: t, ctx: ctx, st: store{Repo: repo, raw: raw}, g: g, r: r, repo: pr, base: base}
}

// queue opens a PR adding its own file, records it, and enqueues its head.
func (f *fixture) queue(branch string) *testPR {
	f.t.Helper()
	f.r.Branch(branch, "main")
	file := "pr/" + branch + ".txt"
	head := f.r.Commit(branch, "work on "+branch, map[string]string{file: branch + "\n"})
	n := f.r.OpenPR(fakegithub.PROptions{Title: "Change " + branch, Head: branch})
	p := &testPR{branch: branch, recordID: "rec-" + branch, head: head, file: file, number: n}
	if _, err := f.st.raw.ExecContext(f.ctx, `INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, head_ref, base_ref, title, snapshot_at, observed_at) VALUES ($1, 'github', 'inst-1', 'rid-1', 'acme/widgets', $2, 'open', $3, $4, 'main', $5, now(), now())`,
		p.recordID, n, head, branch, "Change "+branch); err != nil {
		f.t.Fatalf("insert record: %v", err)
	}
	if _, _, err := f.st.EnsurePRPipeline(f.ctx, core.PRPipelineNew{
		RecordID: p.recordID, OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID,
		State: core.PRPipelineQueued, StateHeadSHA: head,
	}); err != nil {
		f.t.Fatalf("pr pipeline: %v", err)
	}
	e, _, err := f.st.EnqueueQueueEntry(f.ctx, core.QueueEntryNew{
		OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID, RecordID: p.recordID, HeadSHA: head, Priority: 50,
	})
	if err != nil {
		f.t.Fatalf("enqueue: %v", err)
	}
	p.entryID = e.ID
	f.prs = append(f.prs, p)
	return p
}

// batch puts prs in a passed batch on main's tip, gated in isolation, with
// T_1..T_n computed by applying each PR's file in turn.
func (f *fixture) batch(isolation core.QueueIsolationBackend, prs ...*testPR) *core.QueueBatch {
	f.t.Helper()
	tip := f.r.Head("main")
	b, err := f.st.CreateQueueBatch(f.ctx, core.QueueBatchNew{OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID, BaseSHA: tip})
	if err != nil {
		f.t.Fatal(err)
	}
	var ids, trees []string
	tree := f.r.Tree(tip)
	for _, p := range prs {
		ids = append(ids, p.entryID)
		tree = f.r.TreeWithFile(tree, p.file, p.branch+"\n")
		trees = append(trees, tree)
	}
	if err := f.st.AttachQueueEntries(f.ctx, b.ID, ids); err != nil {
		f.t.Fatal(err)
	}
	treesJSON, _ := json.Marshal(trees)
	if _, err := f.st.SetQueueBatchState(f.ctx, b.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{
		CandidateSHA: tip, CandidateRef: "refs/heads/reliant-queue/" + b.ID, ExpectedTreesJSON: treesJSON,
	}); err != nil {
		f.t.Fatal(err)
	}
	if b, err = f.st.SetQueueBatchState(f.ctx, b.ID, core.QueueBatchGating, core.QueueBatchPassed, core.QueueBatchUpdate{
		IsolationBackend: isolation, GateMachineID: "gate-1", VerdictJSON: json.RawMessage(`{"mode":"on","class":"pass","landable":true}`),
	}); err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fixture) lander() *lander.Lander {
	f.t.Helper()
	l, err := lander.New(lander.Deps{
		Store: f.st, Credentials: creds{gh: f.g, id: f.id}, Verifier: f.ver,
		Sleep: func(_ context.Context, d time.Duration) error { f.waits = append(f.waits, d); return nil },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

// request is what the scheduler hands the lander for b.
func (f *fixture) request(b *core.QueueBatch) lander.Request {
	f.t.Helper()
	b, err := f.st.GetQueueBatch(f.ctx, b.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	entries, err := f.st.ListQueueBatchEntries(f.ctx, b.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var trees []string
	_ = json.Unmarshal(b.ExpectedTreesJSON, &trees)
	heads := map[string]string{}
	for _, e := range entries {
		heads[e.ID] = e.HeadSHA
	}
	return lander.Request{Repo: f.repo, Batch: b, Entries: entries, Heads: heads, Trees: trees}
}

func (f *fixture) land(b *core.QueueBatch) lander.Result {
	f.t.Helper()
	res, err := f.lander().Land(f.ctx, f.request(b))
	if err != nil {
		f.t.Fatalf("land: %v", err)
	}
	return res
}

func (f *fixture) entry(p *testPR) *core.QueueEntry {
	f.t.Helper()
	var e core.QueueEntry
	var reason, landed sql.NullString
	if err := f.st.raw.QueryRowContext(f.ctx, `SELECT id, state, eject_reason, landed_sha FROM queue_entries WHERE id = $1`, p.entryID).
		Scan(&e.ID, &e.State, &reason, &landed); err != nil {
		f.t.Fatalf("entry %s: %v", p.entryID, err)
	}
	e.EjectReason, e.LandedSHA = core.QueueEjectReason(reason.String), landed.String
	return &e
}

func (f *fixture) reload(b *core.QueueBatch) *core.QueueBatch {
	f.t.Helper()
	out, err := f.st.GetQueueBatch(f.ctx, b.ID)
	if err != nil || out == nil {
		f.t.Fatalf("batch %s: %v", b.ID, err)
	}
	return out
}

func (f *fixture) queueState() core.PipelineQueueState {
	f.t.Helper()
	r, err := f.st.GetPipelineRepo(f.ctx, f.repo.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return r.QueueState
}

func (f *fixture) verdict(b *core.QueueBatch) map[string]json.RawMessage {
	f.t.Helper()
	var v map[string]json.RawMessage
	if err := json.Unmarshal(f.reload(b).VerdictJSON, &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// batches lists the repository's batches of kind.
func (f *fixture) batches(kind core.QueueBatchKind) []*core.QueueBatch {
	f.t.Helper()
	all, err := f.st.LatestQueueBatches(f.ctx, f.repo.ID, 50)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []*core.QueueBatch
	for _, b := range all {
		if b.Kind == kind {
			out = append(out, b)
		}
	}
	return out
}

func (f *fixture) check(p *testPR) ghport.CheckRun {
	f.t.Helper()
	runs, err := f.g.ListCheckRuns(f.ctx, f.r.ID(), p.head, ghport.ListCheckRuns{Name: "Reliant"})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(runs) != 1 {
		f.t.Fatalf("#%d has %d Reliant checks, want 1", p.number, len(runs))
	}
	return runs[0]
}

func (f *fixture) pipelineState(p *testPR) core.PRPipelineState {
	f.t.Helper()
	row, err := f.st.GetPRPipeline(f.ctx, p.recordID)
	if err != nil || row == nil {
		f.t.Fatalf("pr pipeline: %v", err)
	}
	return row.State
}

func (f *fixture) commit(sha string) ghport.Commit {
	f.t.Helper()
	c, err := f.g.GetCommit(f.ctx, f.r.ID(), sha)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
