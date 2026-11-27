package practions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

const testOrg = "org-1"

// fakeGitHub is GitHub for the actions: PRs by number, their files, and
// every write it was asked to make.
type fakeGitHub struct {
	mu       sync.Mutex
	prs      map[int]practions.PullRequest
	files    map[int]practions.ChangedFiles
	comments []string
	closed   []int
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{prs: map[int]practions.PullRequest{}, files: map[int]practions.ChangedFiles{}}
}

func (g *fakeGitHub) set(number int, head string, files ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pr := g.prs[number]
	pr.Number, pr.State, pr.HeadSHA, pr.BaseRef = number, "open", head, "main"
	g.prs[number] = pr
	var cf []prpolicy.ChangedFile
	blobs := map[string]string{}
	for _, f := range files {
		path, blob, _ := strings.Cut(f, "@")
		cf = append(cf, prpolicy.ChangedFile{Path: path, Status: prpolicy.FileModified, Additions: 10, Deletions: 2})
		blobs[path] = blob
	}
	g.files[number] = practions.ChangedFiles{Diff: prpolicy.NewDiff(cf...), Blobs: blobs}
}

func (g *fakeGitHub) PullRequest(_ context.Context, _ practions.Caller, _ string, number int) (practions.PullRequest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pr, ok := g.prs[number]
	if !ok {
		return pr, errors.New("not found")
	}
	return pr, nil
}

func (g *fakeGitHub) ChangedFiles(_ context.Context, _ practions.Caller, _ string, number int) (practions.ChangedFiles, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.files[number], nil
}

func (g *fakeGitHub) Comment(_ context.Context, _ practions.Caller, _ string, _ int, body string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.comments = append(g.comments, body)
	return nil
}

func (g *fakeGitHub) Close(_ context.Context, _ practions.Caller, _ string, number int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = append(g.closed, number)
	pr := g.prs[number]
	pr.State = "closed"
	g.prs[number] = pr
	return nil
}

type fakeInbox struct {
	mu    sync.Mutex
	items map[string]practions.InboxItem
}

func (f *fakeInbox) Page(_ context.Context, item practions.InboxItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items == nil {
		f.items = map[string]practions.InboxItem{}
	}
	if _, ok := f.items[item.ID]; ok {
		return practions.ErrInboxItemExists
	}
	f.items[item.ID] = item
	return nil
}

// harness is one test's database, fakes and actions, with one enrolled
// repository and one PR on it.
type harness struct {
	t      *testing.T
	ctx    context.Context
	env    *storetest.Env
	gh     *fakeGitHub
	inbox  *fakeInbox
	svc    *practions.Service
	repo   *core.PipelineRepo
	record string
	number int
	now    time.Time
	execs  atomic.Int64
}

func newHarness(t *testing.T, mode core.PipelineRepoMode) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), env: storetest.New(t), gh: newFakeGitHub(), inbox: &fakeInbox{},
		number: 7, now: time.Now().UTC().Truncate(time.Second)}
	h.repo = h.enrol(mode)
	h.record = h.addPR(h.number, "head-1")
	svc, err := practions.New(practions.Deps{Store: h.env.Repo, GitHub: h.gh, Inbox: h.inbox, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func (h *harness) enrol(mode core.PipelineRepoMode) *core.PipelineRepo {
	r, err := h.env.Repo.UpsertPipelineRepo(h.ctx, core.PipelineRepoEnrolment{
		OrgID: testOrg, ProjectID: storetest.SeedProjectID, Provider: "github", AccountKey: "inst-1",
		RepositoryID: "rid-1", Repository: "acme/app", CreatedByUserID: storetest.SeedUserID,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	if mode != r.Mode {
		if r, err = h.env.Repo.SetPipelineRepoMode(h.ctx, r.ID, r.Version, mode); err != nil {
			h.t.Fatal(err)
		}
	}
	return r
}

// addPR records PR number at head, in the pipeline, open on GitHub with a
// code change (so it needs a model).
func (h *harness) addPR(number int, head string) string {
	id := fmt.Sprintf("rec-%d", number)
	if _, err := h.env.SQL.Exec(`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, base_ref, author_login, snapshot_at, observed_at) VALUES ($1, 'github', 'inst-1', 'rid-1', 'acme/app', $2, 'open', $3, 'main', 'alice', now(), now())`,
		id, number, head); err != nil {
		h.t.Fatal(err)
	}
	if _, _, err := h.env.Repo.EnsurePRPipeline(h.ctx, core.PRPipelineNew{RecordID: id, OrgID: testOrg, ProjectID: storetest.SeedProjectID,
		RepoID: h.repo.ID, StateHeadSHA: head}); err != nil {
		h.t.Fatal(err)
	}
	h.gh.set(number, head, "internal/app/service.go@blob-1")
	return id
}

// config sets the repository's pipeline.yaml (as config_json).
func (h *harness) config(cfg string) {
	h.t.Helper()
	r, err := h.env.Repo.SetPipelineRepoConfig(h.ctx, h.repo.ID, h.repo.Version, "cfg-1", json.RawMessage(cfg))
	if err != nil {
		h.t.Fatal(err)
	}
	h.repo = r
}

func (h *harness) row() *core.PRPipeline {
	r, err := h.env.Repo.GetPRPipeline(h.ctx, h.record)
	if err != nil || r == nil {
		h.t.Fatalf("row: %v", err)
	}
	return r
}

type call struct {
	run, node, thread, path string
	params                  map[string]any
	from                    *serveraction.NodeRecord
	provenance              *serveraction.Provenance
	principal               *serveraction.Principal
	key                     string
}

// do runs one action as a top-level node of run-1's built-in workflow.
func (h *harness) do(name string, c call) (*serveraction.Result, error) {
	if c.run == "" {
		c.run = "run-1"
	}
	if c.node == "" {
		c.node = strings.TrimSuffix(strings.TrimPrefix(name, "reliant/pr."), "@1")
	}
	if c.path == "" {
		c.path = c.node
	}
	if c.thread == "" {
		c.thread = "thread-main"
	}
	if c.key == "" {
		c.key = fmt.Sprintf("exec-%d", h.execs.Add(1))
	}
	params := map[string]any{"record_id": h.record}
	for k, v := range c.params {
		params[k] = v
	}
	req := &serveraction.Request{
		Name: name, Params: params, From: c.from,
		Invocation: serveraction.Invocation{RunID: c.run, ChatID: "chat-1", ThreadID: c.thread, NodeID: c.node, NodePath: c.path, ExecutionKey: c.key},
		Principal:  serveraction.Principal{UserID: storetest.SeedUserID, OrgID: testOrg, ProjectID: storetest.SeedProjectID},
		Provenance: serveraction.Provenance{WorkflowRef: "builtin://pr", Source: serveraction.SourceBuiltin},
	}
	if c.provenance != nil {
		req.Provenance = *c.provenance
	}
	if c.principal != nil {
		req.Principal = *c.principal
	}
	h2 := h.svc.Handlers()[name]
	return h2.Handle(h.ctx, req)
}

func (h *harness) must(name string, c call) map[string]any {
	h.t.Helper()
	res, err := h.do(name, c)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return res.Data
}

func refused(t *testing.T, err error, code string) {
	t.Helper()
	e, ok := serveraction.AsError(err)
	if !ok || e.Code != code {
		t.Fatalf("want a %s refusal, got %v", code, err)
	}
}

func finding(id, path, body string) prpolicy.Finding {
	return prpolicy.Finding{ID: id, Severity: prpolicy.SeverityBlocker, Path: path, Body: body}
}

func review(verdict, session string, findings ...prpolicy.Finding) *serveraction.NodeRecord {
	if findings == nil {
		findings = []prpolicy.Finding{}
	}
	var fs []any
	b, _ := json.Marshal(findings)
	_ = json.Unmarshal(b, &fs)
	return &serveraction.NodeRecord{NodeID: "review", Type: "workflow", Role: "reviewer", Review: true, Ran: true,
		Output: map[string]any{"verdict": verdict, "findings": fs, "summary": "s", "session_id": session, "role": "reviewer"}}
}

func verdictCall(from *serveraction.NodeRecord) call {
	return call{node: "verdict", from: from, params: map[string]any{"from": "review"}}
}

func TestLowRiskHeadPassesWithNoModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.set(h.number, "head-1", "docs/guide.md@b1", "README.md@b2")

	got := h.must(serveraction.PRNext, call{node: "decide"})
	if got["next"] != practions.NextEnqueue || got["head"] != "head-1" {
		t.Fatalf("a docs-only head is enqueued with no model: %v", got)
	}
	v, err := h.env.Repo.LatestPRHeadVerdict(h.ctx, h.record, "head-1")
	if err != nil || v == nil || v.Rule != core.PRVerdictRuleNoModelLowRisk || v.Verdict != core.PRHeadVerdictPass ||
		v.RiskTier != core.PRRiskLow || v.ReviewerPrincipal != "reliant/pr.next@1" || v.RunID != "run-1" {
		t.Fatalf("pr.next writes the no-model verdict itself: %+v %v", v, err)
	}
	if r := h.row(); r.State != core.PRPipelineApproved || r.OwnerRunID != "run-1" {
		t.Fatalf("approved and claimed: %+v", r)
	}
	// Asked again (a retry), it answers the same and writes no second verdict.
	if again := h.must(serveraction.PRNext, call{node: "decide"}); again["next"] != practions.NextEnqueue {
		t.Fatalf("again: %v", again)
	}
	vs, _ := h.env.Repo.ListPRHeadVerdicts(h.ctx, h.record)
	if len(vs) != 1 {
		t.Fatalf("one verdict, got %d", len(vs))
	}
}

func TestReviewPassEnqueues(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	got := h.must(serveraction.PRNext, call{node: "decide"})
	if got["next"] != practions.NextReview || got["risk_tier"] != "medium" {
		t.Fatalf("a code change goes to the reviewer: %v", got)
	}
	if r := h.row(); r.State != core.PRPipelineReviewing || r.OwnerRunID != "run-1" || r.NextActionAt == nil {
		t.Fatalf("reviewing, claimed, with a watchdog: %+v", r)
	}
	// Another run is told this head is taken.
	if other := h.must(serveraction.PRNext, call{run: "run-2", node: "decide"}); other["next"] != practions.NextNone {
		t.Fatalf("another run waits: %v", other)
	}

	out := h.must(serveraction.PRVerdict, verdictCall(review("pass", "session-r1")))
	if out["next"] != practions.NextEnqueue || out["verdict"] != "pass" {
		t.Fatalf("a pass enqueues: %v", out)
	}
	v, _ := h.env.Repo.LatestPRHeadVerdict(h.ctx, h.record, "head-1")
	if v == nil || v.Rule != core.PRVerdictRuleModel || v.NodeID != "review" || v.ReviewerPrincipal != "reviewer:session-r1" {
		t.Fatalf("model verdict: %+v", v)
	}
	if r := h.row(); r.State != core.PRPipelineApproved || r.ReviewRound != 1 {
		t.Fatalf("approved: %+v", r)
	}
	// A retry of the same verdict answers the same, with no second row.
	again := h.must(serveraction.PRVerdict, verdictCall(review("pass", "session-r1")))
	if again["next"] != practions.NextEnqueue {
		t.Fatalf("retry: %v", again)
	}
	if vs, _ := h.env.Repo.ListPRHeadVerdicts(h.ctx, h.record); len(vs) != 1 {
		t.Fatalf("one verdict, got %d", len(vs))
	}
}

func TestOwnerDecisionRequiredEscalates(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.config(`{"version": 1, "gate": "task test", "owner_approval": [{"kind": "approve_breaking_proto", "paths": ["internal/app/**"]}]}`)
	h.must(serveraction.PRNext, call{node: "decide"})
	out := h.must(serveraction.PRVerdict, verdictCall(review("pass", "session-r1")))
	if out["next"] != practions.NextEscalate || !strings.Contains(fmt.Sprint(out["reason"]), "approve_breaking_proto") {
		t.Fatalf("a pass without the decision it requires escalates: %v", out)
	}
}

func TestChangesFixPushedAndAttemptCaps(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	const maxFixes = 2
	h.config(`{"version": 1, "gate": "task test", "fix": {"attempts_per_signature": 2}, "review": {"max_rounds": 5}}`)
	blocker := finding("b1", "internal/app/service.go", "nil map write")
	for round := 1; ; round++ {
		head := fmt.Sprintf("head-%d", round)
		next := h.must(serveraction.PRNext, call{node: "decide"})
		if round > maxFixes {
			// Every fix left the same blocker: the attempts at its
			// signature ran out.
			if next["next"] != practions.NextReview {
				t.Fatalf("round %d reviews first: %v", round, next)
			}
			out := h.must(serveraction.PRVerdict, verdictCall(review("changes", fmt.Sprintf("session-r%d", round), blocker)))
			if out["next"] != practions.NextEscalate || !strings.Contains(fmt.Sprint(out["reason"]), "fix attempts left the same failure") {
				t.Fatalf("attempts exhausted escalate: %v", out)
			}
			return
		}
		if next["next"] != practions.NextReview || next["head"] != head {
			t.Fatalf("round %d: %v", round, next)
		}
		out := h.must(serveraction.PRVerdict, verdictCall(review("changes", fmt.Sprintf("session-r%d", round), blocker)))
		if out["next"] != practions.NextFix || len(out["blockers"].([]any)) != 1 {
			t.Fatalf("round %d: changes go to the fixer: %v", round, out)
		}
		if r := h.row(); r.State != core.PRPipelineFixing || r.FixAttempts != round {
			t.Fatalf("round %d: fixing, attempt %d: %+v", round, round, r)
		}
		pushed := h.must(serveraction.PRPushed, call{node: "pushed"})
		if pushed["next"] != practions.NextDecide || pushed["attempt"] != round {
			t.Fatalf("round %d: pushed: %v", round, pushed)
		}
		// The push made a new head, with the file changed.
		h.gh.set(h.number, fmt.Sprintf("head-%d", round+1), fmt.Sprintf("internal/app/service.go@blob-%d", round+1))
	}
}

func TestReviewConverges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.set(h.number, "head-1", "a.go@a1", "b.go@b1")
	fixed := finding("f1", "a.go", "bug in a")
	h.must(serveraction.PRNext, call{node: "decide"})
	h.must(serveraction.PRVerdict, verdictCall(review("changes", "s1", fixed)))
	h.must(serveraction.PRPushed, call{node: "pushed"})
	h.gh.set(h.number, "head-2", "a.go@a2", "b.go@b1") // only a.go changed

	// Round 2 reports a blocker in b.go, which did not change: a late
	// finding, recorded as a nit, so the round passes.
	if next := h.must(serveraction.PRNext, call{node: "decide"}); next["next"] != practions.NextReview {
		t.Fatalf("re-review: %v", next)
	}
	late := finding("l1", "b.go", "style in b")
	out := h.must(serveraction.PRVerdict, verdictCall(review("changes", "s2", late)))
	if out["next"] != practions.NextEnqueue || out["verdict"] != "pass" {
		t.Fatalf("a late finding on unchanged code does not block: %v", out)
	}
	v, _ := h.env.Repo.LatestPRHeadVerdict(h.ctx, h.record, "head-2")
	if !strings.Contains(string(v.FindingsJSON), `"nit"`) {
		t.Fatalf("the late finding is recorded as a nit: %s", v.FindingsJSON)
	}
}

func TestVerdictRefusals(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.must(serveraction.PRNext, call{node: "decide"})
	notReviewer := review("pass", "s1")
	notReviewer.Review, notReviewer.Role = false, ""
	agent := review("pass", "s1")
	agent.Review = false // role kept, but not an engine-owned review node

	cases := []struct {
		name string
		c    call
		code string
	}{
		{"not a reviewer node", verdictCall(notReviewer), serveraction.CodePermissionDenied},
		{"not engine owned", verdictCall(agent), serveraction.CodePermissionDenied},
		{"no such node", call{node: "verdict", params: map[string]any{"from": "review"}}, serveraction.CodeFailedPrecondition},
		{"nested", func() call { c := verdictCall(review("pass", "s1")); c.path = "outer.verdict"; return c }(), serveraction.CodePermissionDenied},
		{"unknown provenance", func() call {
			c := verdictCall(review("pass", "s1"))
			c.provenance = &serveraction.Provenance{WorkflowRef: "pr"}
			return c
		}(), serveraction.CodePermissionDenied},
		{"account workflow", func() call {
			c := verdictCall(review("pass", "s1"))
			c.provenance = &serveraction.Provenance{WorkflowRef: "pr", Source: serveraction.SourceUser}
			return c
		}(), serveraction.CodePermissionDenied},
		{"project file off the base branch", func() call {
			c := verdictCall(review("pass", "s1"))
			c.provenance = &serveraction.Provenance{WorkflowRef: "pr", Source: serveraction.SourceProject, Path: "pr.yaml", CatalogPin: "pin",
				Repo: &serveraction.RepoState{Branch: "feature", Commit: "c1"}}
			return c
		}(), serveraction.CodePermissionDenied},
		{"project file with uncommitted changes", func() call {
			c := verdictCall(review("pass", "s1"))
			c.provenance = &serveraction.Provenance{WorkflowRef: "pr", Source: serveraction.SourceProject, Path: "pr.yaml", CatalogPin: "pin",
				Repo: &serveraction.RepoState{Branch: "main", Commit: "c1", Dirty: true}}
			return c
		}(), serveraction.CodePermissionDenied},
		{"review in the run's own session", verdictCall(review("pass", "thread-main")), serveraction.CodePermissionDenied},
		{"another tenant", func() call {
			c := verdictCall(review("pass", "s1"))
			c.principal = &serveraction.Principal{UserID: "u2", OrgID: "org-2", ProjectID: storetest.SeedProjectID}
			return c
		}(), serveraction.CodePermissionDenied},
	}
	for _, tc := range cases {
		_, err := h.do(serveraction.PRVerdict, tc.c)
		if e, ok := serveraction.AsError(err); !ok || e.Code != tc.code {
			t.Errorf("%s: want %s, got %v", tc.name, tc.code, err)
		}
	}
	if vs, _ := h.env.Repo.ListPRHeadVerdicts(h.ctx, h.record); len(vs) != 0 {
		t.Fatalf("no refused verdict was written: %d", len(vs))
	}

	// The repository's own file at its base branch may write one.
	c := verdictCall(review("pass", "s1"))
	c.provenance = &serveraction.Provenance{WorkflowRef: "pr", Source: serveraction.SourceProject, Path: "pr.yaml", CatalogPin: "pin",
		Repo: &serveraction.RepoState{Branch: "main", Commit: "c1"}}
	if out := h.must(serveraction.PRVerdict, c); out["next"] != practions.NextEnqueue {
		t.Fatalf("the base branch's file is authorized: %v", out)
	}
}

func TestVerdictRefusesASessionThatFixed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.must(serveraction.PRNext, call{node: "decide"})
	h.must(serveraction.PRVerdict, verdictCall(review("changes", "s1", finding("b1", "x.go", "bug"))))
	// The fix is pushed from the thread "fixer".
	h.must(serveraction.PRPushed, call{node: "pushed", thread: "fixer"})
	h.gh.set(h.number, "head-2", "internal/app/service.go@blob-2")
	h.must(serveraction.PRNext, call{node: "decide"})
	_, err := h.do(serveraction.PRVerdict, verdictCall(review("pass", "fixer")))
	refused(t, err, serveraction.CodePermissionDenied)
}

func TestVerdictOnAMovedHeadIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.must(serveraction.PRNext, call{node: "decide"})
	h.gh.set(h.number, "head-2", "internal/app/service.go@blob-2")
	_, err := h.do(serveraction.PRVerdict, verdictCall(review("pass", "s1")))
	refused(t, err, serveraction.CodeAborted)
	// pr.next then starts the new head over.
	if next := h.must(serveraction.PRNext, call{node: "decide"}); next["next"] != practions.NextReview || next["head"] != "head-2" {
		t.Fatalf("the new head is reviewed: %v", next)
	}
}

// racingStore moves the PR's row on between an action's read and its write.
type racingStore struct {
	*db.Repo
	once sync.Once
}

func (r *racingStore) GetPRPipeline(ctx context.Context, id string) (*core.PRPipeline, error) {
	row, err := r.Repo.GetPRPipeline(ctx, id)
	if err == nil && row != nil {
		r.once.Do(func() {
			to := *row
			to.Priority = 60
			_, err = r.TransitionPRPipeline(ctx, id, row.Version, to)
		})
	}
	return row, err
}

func TestALostCompareAndSetIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	svc, err := practions.New(practions.Deps{Store: &racingStore{Repo: h.env.Repo}, GitHub: h.gh, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Handlers()[serveraction.PRNext].Handle(h.ctx, &serveraction.Request{
		Name: serveraction.PRNext, Params: map[string]any{"record_id": h.record},
		Invocation: serveraction.Invocation{RunID: "run-1", NodeID: "decide", NodePath: "decide", ExecutionKey: "k"},
		Principal:  serveraction.Principal{UserID: storetest.SeedUserID, OrgID: testOrg, ProjectID: storetest.SeedProjectID},
	})
	refused(t, err, serveraction.CodeAborted)
	if r := h.row(); r.State != core.PRPipelineAwaitingReview || r.OwnerRunID != "" {
		t.Fatalf("the superseded write changed nothing: %+v", r)
	}
}

func TestShadowModeSuppressesGitHubWrites(t *testing.T) {
	t.Parallel()
	for _, mode := range []core.PipelineRepoMode{core.PipelineRepoModeShadow, core.PipelineRepoModeOn} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, mode)
			out := h.must(serveraction.PRClose, call{params: map[string]any{"evidence": map[string]any{
				"kind": "duplicate", "by": float64(3), "facts": []any{"#7 and #3 have the same head head-1"}}}})
			writes := out["writes"].([]any)
			if len(writes) != 2 {
				t.Fatalf("a comment and a close: %v", writes)
			}
			suppressed := writes[0].(map[string]any)["suppressed"] == true
			if mode == core.PipelineRepoModeShadow {
				if !suppressed || len(h.gh.comments) != 0 || len(h.gh.closed) != 0 {
					t.Fatalf("shadow writes nothing to GitHub: %v %v %v", writes, h.gh.comments, h.gh.closed)
				}
			} else if suppressed || len(h.gh.comments) != 1 || len(h.gh.closed) != 1 ||
				!strings.Contains(h.gh.comments[0], "duplicate by #3") {
				t.Fatalf("on writes: %v %v %v", writes, h.gh.comments, h.gh.closed)
			}
			if r := h.row(); r.State != core.PRPipelineClosed {
				t.Fatalf("closed: %+v", r)
			}
			// Closing again writes nothing more.
			h.must(serveraction.PRClose, call{params: map[string]any{"evidence": "again"}})
			if mode == core.PipelineRepoModeOn && (len(h.gh.comments) != 1 || len(h.gh.closed) != 1) {
				t.Fatalf("a second close writes nothing: %v", h.gh.comments)
			}
		})
	}
}

func TestNeedsPersonPagesAndRepages(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOn)
	out := h.must(serveraction.PRNeedsPerson, call{key: "np-1", params: map[string]any{"reason": "3 fixes failed"}})
	if out["paged"] != true || len(h.gh.comments) != 1 || len(h.inbox.items) != 1 {
		t.Fatalf("a page: an inbox item and one comment: %v %v %v", out, h.gh.comments, h.inbox.items)
	}
	r := h.row()
	if r.State != core.PRPipelineNeedsPerson || r.NeedsPersonReason != "3 fixes failed" || r.NextActionAt == nil ||
		!r.NextActionAt.Equal(h.now.Add(2*time.Hour)) {
		t.Fatalf("needs_person, re-paged in 2h: %+v", r)
	}
	// A retry of that page is the same page.
	h.must(serveraction.PRNeedsPerson, call{key: "np-1", params: map[string]any{"reason": "3 fixes failed"}})
	if len(h.inbox.items) != 1 || len(h.gh.comments) != 1 {
		t.Fatalf("a retried page is not a second one: %v %v", h.inbox.items, h.gh.comments)
	}
	// Before the re-page is due, pr.next waits; at it, it escalates again.
	if next := h.must(serveraction.PRNext, call{node: "decide"}); next["next"] != practions.NextNone {
		t.Fatalf("waiting for a person: %v", next)
	}
	h.now = h.now.Add(2 * time.Hour)
	if next := h.must(serveraction.PRNext, call{node: "decide"}); next["next"] != practions.NextEscalate {
		t.Fatalf("the re-page is due: %v", next)
	}
	h.must(serveraction.PRNeedsPerson, call{key: "np-2", params: map[string]any{"reason": "3 fixes failed"}})
	if len(h.inbox.items) != 2 || len(h.gh.comments) != 1 {
		t.Fatalf("a re-page is a new inbox item and no new comment: %v %v", h.inbox.items, h.gh.comments)
	}
	if since := h.row().NeedsPersonSince; since == nil || !since.Equal(r.NeedsPersonSince.UTC()) {
		t.Fatalf("needs_person_since is kept: %v", since)
	}
}

func TestCheckedRoundsThenEscalate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.must(serveraction.PRNext, call{node: "decide"})
	h.must(serveraction.PRVerdict, verdictCall(review("changes", "s1", finding("b1", "x.go", "bug"))))
	failing := &serveraction.NodeRecord{NodeID: "check", Type: "run", Ran: true, Output: map[string]any{
		"exit_code": float64(1),
		"stdout":    "lint ok\n" + `{"stages": [{"name": "lint", "verdict": "pass"}, {"name": "test", "verdict": "fail", "failures": [{"test": "internal/x TestY", "detail": "want 1 got 2"}]}]}`,
	}}
	checkCall := func(from *serveraction.NodeRecord, key string) call {
		return call{node: "record", key: key, from: from, params: map[string]any{"from": "check"}}
	}
	for round := 1; round <= practions.DefaultCheckRounds; round++ {
		out := h.must(serveraction.PRChecked, checkCall(failing, fmt.Sprintf("chk-%d", round)))
		want := practions.NextFix
		if round == practions.DefaultCheckRounds {
			want = practions.NextEscalate
		}
		fs := out["failures"].([]any)
		if out["next"] != want || out["round"] != round || len(fs) != 1 || fs[0].(map[string]any)["check"] != "test" {
			t.Fatalf("round %d: %v", round, out)
		}
	}
	// A retried check is not another round.
	if out := h.must(serveraction.PRChecked, checkCall(failing, "chk-1")); out["round"] != practions.DefaultCheckRounds {
		t.Fatalf("a retry counts nothing: %v", out)
	}
	passing := &serveraction.NodeRecord{NodeID: "check", Type: "run", Ran: true, Output: map[string]any{"stdout": "all good"}}
	if out := h.must(serveraction.PRChecked, checkCall(passing, "chk-pass")); out["next"] != practions.NextPush {
		t.Fatalf("a passing check pushes: %v", out)
	}
	// Only a run node's output is a check.
	agent := &serveraction.NodeRecord{NodeID: "check", Type: "call_llm", Ran: true, Output: map[string]any{"stdout": "trust me"}}
	_, err := h.do(serveraction.PRChecked, checkCall(agent, "chk-agent"))
	refused(t, err, serveraction.CodeFailedPrecondition)
}

func TestSupersededPRCloses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	// An older open PR, #3, carries the same head.
	h.record = h.addPR(3, "head-1")
	h.record, h.number = "rec-7", 7
	out := h.must(serveraction.PRNext, call{node: "decide"})
	if out["next"] != practions.NextClose || !strings.Contains(fmt.Sprint(out["reason"]), "duplicate by #3") {
		t.Fatalf("a duplicate of an older open PR closes: %v", out)
	}
}

func TestCommentWakesTheFixer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	bot := map[string]any{"id": "c0", "body": "Reliant needs a person", "author": "reliant-labs[bot]"}
	if out := h.must(serveraction.PRNext, call{node: "decide", params: map[string]any{"comment": bot}}); out["next"] != practions.NextReview {
		t.Fatalf("the bot's own comment is not a request: %v", out)
	}
	h2 := newHarness(t, core.PipelineRepoModeShadow)
	person := map[string]any{"id": float64(42), "body": "please rename Foo", "author": "bob", "path": "x.go", "line": float64(3)}
	out := h2.must(serveraction.PRNext, call{node: "decide", params: map[string]any{"comment": person}})
	bs := out["blockers"].([]any)
	if out["next"] != practions.NextFix || len(bs) != 1 || !strings.Contains(fmt.Sprint(bs[0]), "please rename Foo") {
		t.Fatalf("a person's comment becomes the fixer's blocker: %v", out)
	}
}

func TestModeOffAndSwitchOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOff)
	if out := h.must(serveraction.PRNext, call{node: "decide"}); out["next"] != practions.NextNone {
		t.Fatalf("mode off decides nothing: %v", out)
	}
	_, err := h.do(serveraction.PRNeedsPerson, call{params: map[string]any{"reason": "x"}})
	refused(t, err, serveraction.CodeFailedPrecondition)

	// With pipeline.enabled off, the dispatcher refuses before any handler.
	d, err := serveraction.NewDispatcher(serveraction.Config{Enabled: false, Handlers: h.svc.Handlers()})
	if err != nil {
		t.Fatal(err)
	}
	o := d.Dispatch(h.ctx, serveraction.Call{Uses: serveraction.PRNext, Params: map[string]any{"record_id": h.record}})
	if o.Err == nil || o.Err.Code != serveraction.CodeFailedPrecondition || !strings.Contains(o.Err.Message, serveraction.SwitchName) {
		t.Fatalf("switch off refuses: %+v", o)
	}
	if r := h.row(); r.Version != 0 {
		t.Fatalf("nothing was written: %+v", r)
	}
}
