// Copyright (c) 2025 Reliant Labs

// Package lander lands a passed merge-queue batch on main (pipeline redesign
// §3.1–§3.2 step 5, owner decision D1; MVP task M11).
//
// For each entry, in order, it squash-merges the PR with GitHub's own merge
// (PUT /pulls/{n}/merge, merge_method squash, sha = the gated head), so GitHub
// shows the PR as Merged, and then checks main: the merge commit's only parent
// is the previous main (`main^ == main_{i-1}`, nobody else wrote), its tree is
// T_i, and main points at it. After the last merge main's tree is T_n, the
// tree the gate passed. That is the invariant the queue exists for.
//
// Anything else stops landing:
//
//   - main moved before the first merge (a foreign write; a repository with
//     no ruleset allows one): nothing of the batch is on main, so the batch
//     is abandoned and its entries wait to be rebuilt on the new main;
//   - the first PR cannot merge (closed, its head moved, a conflict): it is
//     ejected, the batch abandoned and the rest wait again;
//   - any mismatch, or a PR that cannot merge partway through: the queue is
//     PAUSED (integrity), the incident recorded in the batch's
//     verdict_json.integrity and logged at ERROR (incidents are deferred in
//     the MVP), the entries not yet landed wait again, and main's current
//     tree is gated as a `verify` batch. If that gate is red, the batch's
//     merged commits are reverted newest first, each to the tree before it,
//     a tree the gate already passed; a revert whose main holds anything else
//     on top is not made, and the queue stays paused for a person.
//
// Every write is idempotent on retry: a merge names its head, so a retry
// after a lost response reads as already merged and is checked like any
// other; an entry already landed is skipped. Rate limits and 5xx are
// retried here a few times, then returned, and the scheduler's next tick
// lands the rest.
//
// The lander lands only in mode on, and only a passed batch gated inside an
// isolation boundary (sysbox or vm). The scheduler never calls it otherwise;
// it refuses anyway ([ErrNotOn], [ErrNotLandable]).
//
//forge:exclude-contract: the merge queue's scheduler calls it through its own consumer-side Lander interface (an adapter converts the request); the lander's seams are the consumer-side Store, Credentials and Verifier interfaces
package lander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/display"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// Errors the lander refuses with. Neither has written anything.
var (
	// ErrNotOn: the repository's mode is not on (shadow never lands).
	ErrNotOn = errors.New("lander: the repository's mode is not on")
	// ErrNotLandable: the batch is not passed, or was not gated inside an
	// isolation boundary (runc and checks never land), or is paused.
	ErrNotLandable = errors.New("lander: the batch is not landable")
)

// Store is the queue state the lander reads and writes: every method is
// *db.Repo's of the same name (internal/db/pipeline_*.go).
type Store interface {
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	SetPipelineRepoQueueState(ctx context.Context, id string, version int64, state core.PipelineQueueState, pausedBy string) (*core.PipelineRepo, error)
	// GetQueuePRRecord is the scheduler's read of a PR record by id (M7,
	// internal/db/pipeline_queue_scheduler.go).
	GetQueuePRRecord(ctx context.Context, id string) (*core.PRRecord, error)
	GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error)
	TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error)

	ListQueueBatchEntries(ctx context.Context, batchID string) ([]*core.QueueEntry, error)
	MarkQueueEntryLanded(ctx context.Context, id, landedSHA string) (*core.QueueEntry, error)
	EjectQueueEntry(ctx context.Context, id string, reason core.QueueEjectReason) (*core.QueueEntry, error)
	RequeueQueueEntries(ctx context.Context, batchID string) (int64, error)

	GetQueueBatch(ctx context.Context, id string) (*core.QueueBatch, error)
	CreateQueueBatch(ctx context.Context, b core.QueueBatchNew) (*core.QueueBatch, error)
	SetQueueBatchState(ctx context.Context, id string, from, to core.QueueBatchState, u core.QueueBatchUpdate) (*core.QueueBatch, error)
	MarkQueueBatchLanded(ctx context.Context, id, landedSHA string) (*core.QueueBatch, error)
	PatchQueueBatchVerdict(ctx context.Context, id string, patch json.RawMessage) (*core.QueueBatch, error)
	InsertMainHealth(ctx context.Context, h core.MainHealthRecord) (*core.MainHealthRecord, error)
}

// Identity is who a landing writes as.
type Identity struct {
	// Unenforced: the Reliant App, because the Queue App is absent. No
	// ruleset can tell the queue's writes from any agent's.
	Unenforced bool
	// Login is the bot GitHub shows.
	Login string
}

// Credentials picks the GitHub client a repository's landing writes with
// (queuetoken.Clients: the Queue App, else the Reliant App, unenforced).
type Credentials interface {
	Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, Identity, error)
}

// VerifyClass is a verify gate's verdict.
type VerifyClass string

const (
	VerifyPass     VerifyClass = "pass"
	VerifyFail     VerifyClass = "fail"
	VerifyUnproven VerifyClass = "unproven"
)

// VerifyRequest gates main's own tree after an integrity failure.
type VerifyRequest struct {
	Repo *core.PipelineRepo
	// Batch is the verify batch, gating.
	Batch *core.QueueBatch
	// SHA is main's tip, the commit to gate.
	SHA string
}

// VerifyResult is a verify gate's verdict.
type VerifyResult struct {
	Class     VerifyClass
	Isolation core.QueueIsolationBackend
	Machine   string
	Reason    string
}

// Verifier gates main's tree (the scheduler's gate, behind an adapter). An
// error means no verdict.
type Verifier interface {
	Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error)
}

// Request is a passed batch, in landing order. Its fields are exactly the
// scheduler's mergequeue.LandRequest, so the scheduler's adapter converts one
// into the other with lander.Request(req).
type Request struct {
	Repo    *core.PipelineRepo
	Batch   *core.QueueBatch
	Entries []*core.QueueEntry
	// Heads are the heads to merge, by entry id: the reviewed head, or a
	// regenerated entry's new head.
	Heads map[string]string
	// Trees are T_1..T_n, checked after each squash merge.
	Trees []string
}

// Outcome is how a landing ended.
type Outcome string

const (
	// OutcomeLanded: every entry landed and main's tree is T_n.
	OutcomeLanded Outcome = "landed"
	// OutcomeStaleBase: main moved before the first merge; nothing landed,
	// the batch was abandoned and its entries wait again.
	OutcomeStaleBase Outcome = "stale_base"
	// OutcomeEjected: the first PR could not merge; it was ejected, nothing
	// landed, and the rest wait again.
	OutcomeEjected Outcome = "ejected"
	// OutcomePaused: an integrity failure partway; the queue is paused.
	OutcomePaused Outcome = "paused"
)

// EntryResult is one entry's landing.
type EntryResult struct {
	EntryID   string `json:"entry_id"`
	Number    int    `json:"number"`
	Head      string `json:"head"`
	LandedSHA string `json:"landed_sha,omitempty"`
	// Status is landed, ejected, waiting (requeued) or reverted.
	Status string `json:"status"`
}

// Result is what a landing did. It is recorded in the batch's
// verdict_json.landing.
type Result struct {
	Outcome Outcome       `json:"outcome"`
	Entries []EntryResult `json:"entries"`
	// FinalSHA is main after the batch's last merge (OutcomeLanded).
	FinalSHA string `json:"final_sha,omitempty"`
	// TreeChecked is true when every merge's parent and tree checked out.
	TreeChecked bool     `json:"tree_checked"`
	Unenforced  bool     `json:"unenforced"`
	Identity    string   `json:"identity,omitempty"`
	Integrity   *Problem `json:"integrity,omitempty"`
	// VerifyBatchID is the verify batch an integrity failure gated.
	VerifyBatchID string `json:"verify_batch_id,omitempty"`
	// Reverted are the revert commits written, newest entry first.
	Reverted []string `json:"reverted,omitempty"`
}

// Problem is an integrity failure: what was expected and what main held.
type Problem struct {
	Cause          string `json:"cause"`
	EntryID        string `json:"entry_id,omitempty"`
	Number         int    `json:"number,omitempty"`
	ExpectedParent string `json:"expected_parent,omitempty"`
	ExpectedTree   string `json:"expected_tree,omitempty"`
	MergeSHA       string `json:"merge_sha,omitempty"`
	ActualParents  string `json:"actual_parents,omitempty"`
	ActualTree     string `json:"actual_tree,omitempty"`
	MainSHA        string `json:"main_sha,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// Deps are the lander's collaborators.
type Deps struct {
	Store       Store
	Credentials Credentials
	// Verifier gates main after an integrity failure. nil records the verify
	// batch and leaves it for a person (the queue stays paused).
	//
	// forge:optional-dep
	Verifier Verifier
	// Display shows landings and ejections on GitHub. nil is a
	// display.Projector over Store.
	//
	// forge:optional-dep
	Display Display
	// forge:optional-dep
	Logger *slog.Logger
	// forge:optional-dep
	Now func() time.Time
	// Sleep waits between retries; tests shrink it.
	//
	// forge:optional-dep
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxAttempts bounds each GitHub write's retries (default 5).
	//
	// forge:optional-dep
	MaxAttempts int
	// MaxWait caps one wait for a rate limit's reset (default 1 minute):
	// longer, and the landing returns to be resumed by a later tick.
	//
	// forge:optional-dep
	MaxWait time.Duration
}

// Display publishes the queue's display (display.Projector).
type Display interface {
	Publish(ctx context.Context, repo *core.PipelineRepo, gh ghport.Client, batchID string, views []display.View) error
}

// Lander lands batches. It is safe for concurrent use across repositories;
// the scheduler's per-repository leader lock keeps one landing per
// repository at a time.
type Lander struct{ d Deps }

// New returns a lander.
func New(d Deps) (*Lander, error) {
	if d.Store == nil || d.Credentials == nil {
		return nil, errors.New("lander: a store and credentials are required")
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleep
	}
	if d.MaxAttempts <= 0 {
		d.MaxAttempts = 5
	}
	if d.MaxWait <= 0 {
		d.MaxWait = time.Minute
	}
	if d.Display == nil {
		d.Display = &display.Projector{Store: d.Store, Logger: d.Logger}
	}
	return &Lander{d: d}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// landing is one Land call's state.
type landing struct {
	l       *Lander
	repo    *core.PipelineRepo
	gh      ghport.Repo
	client  ghport.Client
	batch   *core.QueueBatch
	entries []*core.QueueEntry
	heads   map[string]string
	trees   []string
	records []*core.PRRecord
	res     Result
}

// Land lands req's batch. See the package doc. A returned error with a nil
// integrity problem is retryable: nothing inconsistent was left, and the
// next call resumes where this one stopped.
func (l *Lander) Land(ctx context.Context, req Request) (Result, error) {
	if req.Repo == nil || req.Batch == nil {
		return Result{}, errors.New("lander: a repository and a batch are required")
	}
	repo, err := l.d.Store.GetPipelineRepo(ctx, req.Repo.ID)
	if err != nil {
		return Result{}, err
	}
	if repo == nil || repo.Mode != core.PipelineRepoModeOn {
		return Result{}, ErrNotOn
	}
	if repo.QueueState == core.PipelineQueuePaused {
		return Result{}, fmt.Errorf("%w: the queue is paused", ErrNotLandable)
	}
	batch, err := l.d.Store.GetQueueBatch(ctx, req.Batch.ID)
	if err != nil {
		return Result{}, err
	}
	if err := landable(batch); err != nil {
		return Result{}, err
	}
	if len(req.Entries) == 0 || len(req.Trees) != len(req.Entries) {
		return Result{}, fmt.Errorf("lander: batch %s has %d entries and %d expected trees", batch.ID, len(req.Entries), len(req.Trees))
	}
	gh, err := ghport.ParseRepo(repo.Repository)
	if err != nil {
		return Result{}, err
	}
	client, id, err := l.d.Credentials.Client(ctx, repo)
	if err != nil {
		return Result{}, fmt.Errorf("lander: GitHub client for %s: %w", repo.Repository, err)
	}
	g := &landing{l: l, repo: repo, gh: gh, client: client, batch: batch, entries: req.Entries, heads: req.Heads, trees: req.Trees}
	g.res.Unenforced, g.res.Identity = id.Unenforced, id.Login
	if err := g.patch(ctx, map[string]any{"unenforced": id.Unenforced}); err != nil {
		return g.res, err
	}
	for _, e := range req.Entries {
		rec, err := l.d.Store.GetQueuePRRecord(ctx, e.RecordID)
		if err != nil {
			return g.res, err
		}
		if rec == nil {
			return g.res, fmt.Errorf("lander: entry %s's PR record %s is gone", e.ID, e.RecordID)
		}
		g.records = append(g.records, rec)
	}
	return g.run(ctx)
}

// landable is the defensive half of the scheduler's rule: a passed normal
// batch, gated inside an isolation boundary.
func landable(b *core.QueueBatch) error {
	switch {
	case b == nil:
		return fmt.Errorf("%w: no such batch", ErrNotLandable)
	case b.State != core.QueueBatchPassed:
		return fmt.Errorf("%w: batch %s is %s, not passed", ErrNotLandable, b.ID, b.State)
	case b.IsolationBackend != core.QueueIsolationSysbox && b.IsolationBackend != core.QueueIsolationVM &&
		(b.IsolationBackend != core.QueueIsolationRunc || !unisolatedAccepted(b)):
		return fmt.Errorf("%w: batch %s was gated in %q, not an isolation boundary", ErrNotLandable, b.ID, b.IsolationBackend)
	case b.Kind != core.QueueBatchNormal && b.Kind != core.QueueBatchBisect && b.Kind != core.QueueBatchRerun:
		return fmt.Errorf("%w: batch %s is a %s batch", ErrNotLandable, b.ID, b.Kind)
	}
	return nil
}

// unisolatedAccepted reports the scheduler's mark on a runc batch whose
// repository opted in with gate.allow_unisolated (owner, 2026-11-26: until
// isolated gate machines exist). The database lets a runc batch land only
// with it.
func unisolatedAccepted(b *core.QueueBatch) bool {
	var v struct {
		UnisolatedAccepted bool `json:"unisolated_accepted"`
	}
	return json.Unmarshal(b.VerdictJSON, &v) == nil && v.UnisolatedAccepted
}

func (g *landing) head(e *core.QueueEntry) string {
	if h := g.heads[e.ID]; h != "" {
		return h
	}
	return e.HeadSHA
}

func (g *landing) run(ctx context.Context) (Result, error) {
	previous := g.batch.BaseSHA
	landedAny := false
	for i, e := range g.entries {
		rec := g.records[i]
		head := g.head(e)
		if e.State == core.QueueEntryLanded && e.LandedSHA != "" {
			g.res.Entries = append(g.res.Entries, EntryResult{EntryID: e.ID, Number: rec.Number, Head: head, LandedSHA: e.LandedSHA, Status: "landed"})
			previous, landedAny = e.LandedSHA, true
			continue
		}
		if e.State != core.QueueEntryBatched {
			return g.stop(ctx, i, landedAny, &Problem{Cause: "entry_left_queue", Detail: "the entry is " + string(e.State) + ", not batched"}, "")
		}
		tip, err := g.mainTip(ctx)
		if err != nil {
			return g.res, err
		}
		var mergeSHA string
		var ejectReason core.QueueEjectReason
		var problem *Problem
		if tip != previous {
			// A landing that stopped after GitHub merged this PR and before
			// the merge was recorded left main at that merge: adopt it, and
			// check it like any other. Anything else is another writer.
			if !g.mergedAsTip(ctx, rec, head, tip) {
				return g.stop(ctx, i, landedAny, &Problem{Cause: "main_moved", ExpectedParent: previous, MainSHA: tip,
					Detail: "the base branch moved before this merge: another writer"}, "")
			}
			mergeSHA = tip
		} else if mergeSHA, ejectReason, problem, err = g.merge(ctx, i, rec, head); err != nil {
			return g.res, err
		}
		if problem != nil {
			return g.stop(ctx, i, landedAny, problem, ejectReason)
		}
		// The merge happened: record it before judging it, so the record
		// matches GitHub whatever the checks say.
		if _, err := g.l.d.Store.MarkQueueEntryLanded(ctx, e.ID, mergeSHA); err != nil && !errors.Is(err, core.ErrPipelineVersionConflict) {
			return g.res, err
		}
		landedAny = true
		g.res.Entries = append(g.res.Entries, EntryResult{EntryID: e.ID, Number: rec.Number, Head: head, LandedSHA: mergeSHA, Status: "landed"})
		g.afterMerge(ctx, e, rec, head, mergeSHA)
		if problem, err := g.checkMerge(ctx, i, previous, mergeSHA); err != nil {
			return g.res, err
		} else if problem != nil {
			problem.EntryID, problem.Number = e.ID, rec.Number
			return g.pause(ctx, problem)
		}
		previous = mergeSHA
	}
	if _, err := g.l.d.Store.MarkQueueBatchLanded(ctx, g.batch.ID, previous); err != nil {
		return g.res, err
	}
	if _, err := g.l.d.Store.InsertMainHealth(ctx, core.MainHealthRecord{
		OrgID: g.repo.OrgID, ProjectID: g.repo.ProjectID, RepoID: g.repo.ID, SHA: previous,
		Source: core.MainHealthGate, Verdict: core.MainHealthPass, BatchID: g.batch.ID,
	}); err != nil {
		return g.res, err
	}
	g.res.Outcome, g.res.FinalSHA, g.res.TreeChecked = OutcomeLanded, previous, true
	return g.res, g.record(ctx)
}

// merge squash-merges entry i's PR at head, retrying rate limits and 5xx.
// A PR that cannot merge returns a problem (and the reason to eject it, if
// it is the PR's doing), never an error.
func (g *landing) merge(ctx context.Context, i int, rec *core.PRRecord, head string) (sha string, eject core.QueueEjectReason, p *Problem, err error) {
	e := g.entries[i]
	req := ghport.MergeRequest{
		Method: ghport.MergeSquash, ExpectedHeadSHA: head,
		CommitTitle:   fmt.Sprintf("%s (#%d)", strings.TrimSpace(rec.Title), rec.Number),
		CommitMessage: fmt.Sprintf("Reliant-PR: %s#%d head=%s\nReliant-Batch: %s", g.repo.Repository, rec.Number, head, g.batch.ID),
	}
	if strings.TrimSpace(rec.Title) == "" {
		req.CommitTitle = ""
	}
	var res ghport.MergeResult
	err = g.retry(ctx, func() error {
		var err error
		res, err = g.client.MergePullRequest(ctx, g.gh, rec.Number, req)
		return err
	})
	var am *ghport.AlreadyMergedError
	var hm *ghport.HeadMismatchError
	var nm *ghport.NotMergeableError
	base := &Problem{EntryID: e.ID, Number: rec.Number}
	switch {
	case err == nil:
		return res.SHA, "", nil, nil
	case errors.As(err, &am):
		if am.HeadSHA != "" && am.HeadSHA != head {
			base.Cause, base.MergeSHA = "merged_elsewhere", am.MergeCommitSHA
			base.Detail = fmt.Sprintf("the PR merged at head %s, not the gated %s", am.HeadSHA, head)
			return "", "", base, nil
		}
		// A retry after a lost response, or a merge another writer made of
		// exactly this head: done, and checked like any merge.
		return am.MergeCommitSHA, "", nil, nil
	case errors.As(err, &hm):
		base.Cause, base.Detail = "head_moved", hm.Error()
		return "", core.QueueEjectHeadMoved, base, nil
	case errors.As(err, &nm):
		base.Cause, base.Detail = "not_mergeable_"+string(nm.Reason), nm.Error()
		switch nm.Reason {
		case ghport.NotMergeableClosed:
			return "", core.QueueEjectClosed, base, nil
		case ghport.NotMergeableConflict:
			return "", core.QueueEjectConflict, base, nil
		}
		return "", "", base, nil
	case ghport.IsNotFound(err):
		base.Cause, base.Detail = "pr_gone", err.Error()
		return "", core.QueueEjectClosed, base, nil
	}
	return "", "", nil, fmt.Errorf("lander: merging %s#%d: %w", g.repo.Repository, rec.Number, err)
}

// mergedAsTip reports whether the PR already merged, at head, as main's tip.
func (g *landing) mergedAsTip(ctx context.Context, rec *core.PRRecord, head, tip string) bool {
	var pr ghport.PullRequest
	if err := g.retry(ctx, func() error {
		var err error
		pr, err = g.client.GetPullRequest(ctx, g.gh, rec.Number)
		return err
	}); err != nil {
		return false
	}
	return pr.Merged && pr.MergeCommitSHA == tip && pr.HeadSHA == head
}

// checkMerge is §3.2 step 5.2: the merge commit's one parent is the previous
// main, its tree is T_i, and main points at it.
func (g *landing) checkMerge(ctx context.Context, i int, previous, mergeSHA string) (*Problem, error) {
	var c ghport.Commit
	if err := g.retry(ctx, func() error {
		var err error
		c, err = g.client.GetCommit(ctx, g.gh, mergeSHA)
		return err
	}); err != nil {
		return nil, err
	}
	tip, err := g.mainTip(ctx)
	if err != nil {
		return nil, err
	}
	p := &Problem{ExpectedParent: previous, ExpectedTree: g.trees[i], MergeSHA: mergeSHA,
		ActualParents: strings.Join(c.Parents, ","), ActualTree: c.TreeSHA, MainSHA: tip}
	switch {
	case len(c.Parents) != 1 || c.Parents[0] != previous:
		p.Cause = "parent_mismatch"
		p.Detail = "main^ is not the previous main: another writer moved main between merges"
	case c.TreeSHA != g.trees[i]:
		p.Cause = "tree_mismatch"
		p.Detail = "GitHub's squash merge wrote a different tree than the gated candidate's"
	case tip != mergeSHA:
		p.Cause = "main_moved"
		p.Detail = "main moved past the merge before the queue re-read it: another writer"
	default:
		return nil, nil
	}
	return p, nil
}

// afterMerge is the bookkeeping of a merged entry that never decides
// anything: the branch deleted, the PR's pipeline row landed, the display.
func (g *landing) afterMerge(ctx context.Context, e *core.QueueEntry, rec *core.PRRecord, head, mergeSHA string) {
	if rec.HeadRef != "" && rec.HeadRef != baseBranch(g.repo) {
		if err := g.retry(ctx, func() error { return ghport.DeleteBranch(ctx, g.client, g.gh, rec.HeadRef) }); err != nil {
			g.l.d.Logger.WarnContext(ctx, "merge queue could not delete a landed PR's branch",
				"repository", g.repo.Repository, "number", rec.Number, "branch", rec.HeadRef, "err", err)
		}
	}
	if err := g.transition(ctx, e.RecordID, func(p *core.PRPipeline) {
		p.State, p.StateHeadSHA, p.NextAction, p.NextActionAt = core.PRPipelineLanded, head, "", nil
	}); err != nil {
		g.l.d.Logger.WarnContext(ctx, "merge queue could not mark a PR landed", "record_id", e.RecordID, "err", err)
	}
	_ = g.l.d.Display.Publish(ctx, g.repo, g.client, g.batch.ID, []display.View{{
		Number: rec.Number, Head: head, State: display.StateLanded, BatchID: g.batch.ID, LandedSHA: mergeSHA, EntryID: e.ID,
	}})
}

func baseBranch(r *core.PipelineRepo) string {
	if r.BaseBranch == "" {
		return "main"
	}
	return r.BaseBranch
}

func (g *landing) mainTip(ctx context.Context) (string, error) {
	var ref ghport.Ref
	err := g.retry(ctx, func() error {
		var err error
		ref, err = g.client.GetRef(ctx, g.gh, ghport.BranchRef(baseBranch(g.repo)))
		return err
	})
	return ref.SHA, err
}

// stop handles entry i failing before it merged. With nothing of the batch
// on main it abandons the batch quietly; partway, it pauses.
func (g *landing) stop(ctx context.Context, i int, landedAny bool, p *Problem, eject core.QueueEjectReason) (Result, error) {
	e, rec := g.entries[i], g.records[i]
	if p.EntryID == "" {
		p.EntryID, p.Number = e.ID, rec.Number
	}
	if eject != "" {
		if err := g.eject(ctx, e, rec, eject, p.Detail); err != nil {
			return g.res, err
		}
		g.res.Entries = append(g.res.Entries, EntryResult{EntryID: e.ID, Number: rec.Number, Head: g.head(e), Status: "ejected"})
	}
	if landedAny {
		return g.pause(ctx, p)
	}
	// Nothing of this batch is on main: main is whatever it was, and the
	// rest are rebuilt on it by a later batch.
	g.res.Outcome = OutcomeStaleBase
	if eject != "" {
		g.res.Outcome = OutcomeEjected
	}
	g.res.Integrity = p
	if err := g.abandon(ctx); err != nil {
		return g.res, err
	}
	g.l.d.Logger.WarnContext(ctx, "merge queue did not land a batch: nothing of it reached main",
		"repository", g.repo.Repository, "batch_id", g.batch.ID, "cause", p.Cause, "number", p.Number)
	return g.res, g.record(ctx)
}

// eject takes an entry out of the queue, wakes its PR and shows why.
func (g *landing) eject(ctx context.Context, e *core.QueueEntry, rec *core.PRRecord, reason core.QueueEjectReason, detail string) error {
	if _, err := g.l.d.Store.EjectQueueEntry(ctx, e.ID, reason); err != nil && !errors.Is(err, core.ErrPipelineVersionConflict) {
		return err
	}
	now := g.l.d.Now().UTC()
	if err := g.transition(ctx, e.RecordID, func(p *core.PRPipeline) { p.NextAction, p.NextActionAt = "decide", &now }); err != nil {
		g.l.d.Logger.WarnContext(ctx, "merge queue could not wake an ejected PR", "record_id", e.RecordID, "err", err)
	}
	_ = g.l.d.Display.Publish(ctx, g.repo, g.client, g.batch.ID, []display.View{{
		Number: rec.Number, Head: g.head(e), State: display.StateEjected, Reason: string(reason),
		Detail: detail, BatchID: g.batch.ID, EntryID: e.ID,
	}})
	return nil
}

// abandon ends the batch without landing it and puts its unlanded entries
// back to waiting.
func (g *landing) abandon(ctx context.Context) error {
	if _, err := g.l.d.Store.SetQueueBatchState(ctx, g.batch.ID, core.QueueBatchPassed, core.QueueBatchAbandoned, core.QueueBatchUpdate{}); err != nil &&
		!errors.Is(err, core.ErrPipelineVersionConflict) {
		return err
	}
	n, err := g.l.d.Store.RequeueQueueEntries(ctx, g.batch.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		for i, e := range g.entries {
			if e.State == core.QueueEntryBatched && !g.settled(e.ID) {
				g.res.Entries = append(g.res.Entries, EntryResult{EntryID: e.ID, Number: g.records[i].Number, Head: g.head(e), Status: "waiting"})
			}
		}
	}
	return nil
}

func (g *landing) settled(entryID string) bool {
	for _, r := range g.res.Entries {
		if r.EntryID == entryID {
			return true
		}
	}
	return false
}

// pause is the integrity path: pause the queue, record the incident, abandon
// the rest of the batch, gate main as a verify batch, and revert this
// batch's merges if that gate is red.
func (g *landing) pause(ctx context.Context, p *Problem) (Result, error) {
	g.res.Outcome, g.res.Integrity = OutcomePaused, p
	if p.MainSHA == "" {
		tip, err := g.mainTip(ctx)
		if err != nil {
			return g.res, err
		}
		p.MainSHA = tip
	}
	g.l.d.Logger.ErrorContext(ctx, "merge queue integrity incident: landing stopped and the queue is paused",
		"repository", g.repo.Repository, "batch_id", g.batch.ID, "cause", p.Cause, "number", p.Number,
		"expected_parent", p.ExpectedParent, "expected_tree", p.ExpectedTree, "merge_sha", p.MergeSHA,
		"actual_parents", p.ActualParents, "actual_tree", p.ActualTree, "main_sha", p.MainSHA, "detail", p.Detail)
	if err := g.setQueueState(ctx, core.PipelineQueuePaused); err != nil {
		return g.res, err
	}
	if err := g.abandon(ctx); err != nil {
		return g.res, err
	}
	if err := g.record(ctx); err != nil {
		return g.res, err
	}
	verify, err := g.l.d.Store.CreateQueueBatch(ctx, core.QueueBatchNew{
		OrgID: g.repo.OrgID, ProjectID: g.repo.ProjectID, RepoID: g.repo.ID, BaseSHA: p.MainSHA,
		ParentBatchID: g.batch.ID, Kind: core.QueueBatchVerify,
	})
	if err != nil {
		return g.res, err
	}
	g.res.VerifyBatchID = verify.ID
	if err := g.record(ctx); err != nil {
		return g.res, err
	}
	if g.l.d.Verifier == nil {
		g.l.d.Logger.ErrorContext(ctx, "merge queue has no verify gate: main's tree waits for a person",
			"repository", g.repo.Repository, "verify_batch_id", verify.ID, "main_sha", p.MainSHA)
		return g.res, nil
	}
	return g.verify(ctx, verify, p.MainSHA)
}

func (g *landing) verify(ctx context.Context, b *core.QueueBatch, sha string) (Result, error) {
	st := g.l.d.Store
	b, err := st.SetQueueBatchState(ctx, b.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{
		CandidateSHA: sha, CandidateRef: ghport.BranchRef(baseBranch(g.repo)),
	})
	if err != nil {
		return g.res, err
	}
	vr, verr := g.l.d.Verifier.Verify(ctx, VerifyRequest{Repo: g.repo, Batch: b, SHA: sha})
	if verr != nil {
		vr = VerifyResult{Class: VerifyUnproven, Reason: verr.Error()}
	}
	to, health := core.QueueBatchUnproven, core.MainHealthUnproven
	switch vr.Class {
	case VerifyPass:
		to, health = core.QueueBatchPassed, core.MainHealthPass
	case VerifyFail:
		to, health = core.QueueBatchFailed, core.MainHealthFail
	}
	isolation := vr.Isolation
	if isolation == core.QueueIsolationRunc && to == core.QueueBatchPassed {
		// A runc pass is not a pass the queue may act on.
		to, health, vr.Reason = core.QueueBatchUnproven, core.MainHealthUnproven, "gated in runc, not an isolation boundary"
	}
	verdict, _ := json.Marshal(map[string]any{"verify": map[string]any{"class": vr.Class, "reason": vr.Reason, "of_batch": g.batch.ID}})
	if _, err := st.SetQueueBatchState(ctx, b.ID, core.QueueBatchGating, to, core.QueueBatchUpdate{
		IsolationBackend: isolation, GateMachineID: vr.Machine, VerdictJSON: verdict,
	}); err != nil {
		return g.res, err
	}
	if _, err := st.InsertMainHealth(ctx, core.MainHealthRecord{
		OrgID: g.repo.OrgID, ProjectID: g.repo.ProjectID, RepoID: g.repo.ID, SHA: sha,
		Source: core.MainHealthGate, Verdict: health, BatchID: b.ID, Signature: vr.Reason,
	}); err != nil {
		return g.res, err
	}
	switch to {
	case core.QueueBatchPassed:
		// Main is green as it stands: the queue resumes (§3.2).
		return g.res, g.setQueueState(ctx, core.PipelineQueueRunning)
	case core.QueueBatchFailed:
		complete, err := g.revert(ctx)
		if rerr := g.record(ctx); err == nil {
			err = rerr
		}
		if err != nil || !complete {
			return g.res, err
		}
		return g.res, g.setQueueState(ctx, core.PipelineQueueRunning)
	}
	// Unproven: no verdict on main; a person resumes the queue.
	return g.res, nil
}

// revert reverts this batch's merges newest first, each to the tree before
// it. A merge is reverted only while main's tree is exactly the tree it made,
// so the revert restores the previous tree and nothing else; when main holds
// anything on top (another writer's commit) it stops, and complete is false.
func (g *landing) revert(ctx context.Context) (complete bool, err error) {
	var landed []EntryResult
	for _, r := range g.res.Entries {
		if r.Status == "landed" {
			landed = append(landed, r)
		}
	}
	for k := len(landed) - 1; k >= 0; k-- {
		r := landed[k]
		tip, err := g.mainTip(ctx)
		if err != nil {
			return false, err
		}
		var tipCommit, merged, parent ghport.Commit
		if err := g.retry(ctx, func() error {
			var err error
			if tipCommit, err = g.client.GetCommit(ctx, g.gh, tip); err != nil {
				return err
			}
			if merged, err = g.client.GetCommit(ctx, g.gh, r.LandedSHA); err != nil {
				return err
			}
			if len(merged.Parents) == 0 {
				return fmt.Errorf("lander: %s has no parent", r.LandedSHA)
			}
			parent, err = g.client.GetCommit(ctx, g.gh, merged.Parents[0])
			return err
		}); err != nil {
			return false, err
		}
		if tipCommit.TreeSHA != merged.TreeSHA {
			g.l.d.Logger.ErrorContext(ctx, "merge queue cannot revert a landed PR: main holds other changes on top of it",
				"repository", g.repo.Repository, "number", r.Number, "landed_sha", r.LandedSHA, "main_sha", tip)
			return false, nil
		}
		var c ghport.Commit
		msg := fmt.Sprintf("Revert #%d: the verify gate on main was red\n\nThis reverts commit %s, restoring the tree the gate passed before it.\nReliant-Revert: batch %s", r.Number, r.LandedSHA, g.batch.ID)
		if err := g.retry(ctx, func() error {
			var err error
			c, err = g.client.CreateCommit(ctx, g.gh, ghport.NewCommit{Message: msg, TreeSHA: parent.TreeSHA, Parents: []string{tip}})
			return err
		}); err != nil {
			return false, err
		}
		if err := g.retry(ctx, func() error {
			return g.client.UpdateRef(ctx, g.gh, ghport.BranchRef(baseBranch(g.repo)), c.SHA, false)
		}); err != nil {
			if errors.Is(err, ghport.ErrNotFastForward) {
				return false, nil
			}
			return false, err
		}
		g.res.Reverted = append(g.res.Reverted, c.SHA)
		for i := range g.res.Entries {
			if g.res.Entries[i].EntryID == r.EntryID {
				g.res.Entries[i].Status = "reverted"
			}
		}
		now := g.l.d.Now().UTC()
		for i, e := range g.entries {
			if e.ID == r.EntryID {
				if err := g.transition(ctx, e.RecordID, func(p *core.PRPipeline) {
					p.State, p.NeedsPersonReason, p.NeedsPersonSince = core.PRPipelineNeedsPerson, "reverted: the verify gate on main was red after it landed", &now
				}); err != nil {
					g.l.d.Logger.WarnContext(ctx, "merge queue could not flag a reverted PR", "record_id", e.RecordID, "err", err)
				}
				_ = g.l.d.Display.Publish(ctx, g.repo, g.client, g.batch.ID, []display.View{{
					Number: g.records[i].Number, Head: g.head(e), State: display.StateNeedsPerson, EntryID: e.ID,
					Reason: "reverted: the verify gate on main was red after it landed", Detail: "Revert commit: " + c.SHA,
				}})
			}
		}
	}
	return true, nil
}

// retry runs fn, waiting out rate limits and 5xx up to MaxAttempts times.
func (g *landing) retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		ok, notBefore := ghport.Retryable(err)
		if !ok || attempt >= g.l.d.MaxAttempts {
			return err
		}
		wait := time.Duration(attempt) * time.Second
		if !notBefore.IsZero() {
			wait = notBefore.Sub(g.l.d.Now())
		}
		if wait > g.l.d.MaxWait {
			return err
		}
		if wait < 0 {
			wait = 0
		}
		if serr := g.l.d.Sleep(ctx, wait); serr != nil {
			return serr
		}
	}
}

// transitionTries bounds the pr_pipeline compare-and-set retries.
const transitionTries = 3

func (g *landing) transition(ctx context.Context, recordID string, change func(*core.PRPipeline)) error {
	for range transitionTries {
		p, err := g.l.d.Store.GetPRPipeline(ctx, recordID)
		if err != nil || p == nil {
			return err
		}
		to := *p
		change(&to)
		_, err = g.l.d.Store.TransitionPRPipeline(ctx, recordID, p.Version, to)
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("pr pipeline %s: the row kept changing", recordID)
}

func (g *landing) setQueueState(ctx context.Context, state core.PipelineQueueState) error {
	for range transitionTries {
		r, err := g.l.d.Store.GetPipelineRepo(ctx, g.repo.ID)
		if err != nil || r == nil {
			return err
		}
		if r.QueueState == state {
			return nil
		}
		_, err = g.l.d.Store.SetPipelineRepoQueueState(ctx, r.ID, r.Version, state, "")
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("pipeline repo %s: the row kept changing", g.repo.ID)
}

// record writes the landing so far into the batch's verdict_json.landing.
func (g *landing) record(ctx context.Context) error {
	patch := map[string]any{"landing": g.res, "unenforced": g.res.Unenforced}
	if g.res.Integrity != nil && g.res.Outcome == OutcomePaused {
		patch["integrity"] = g.res.Integrity
	}
	return g.patch(ctx, patch)
}

func (g *landing) patch(ctx context.Context, doc map[string]any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = g.l.d.Store.PatchQueueBatchVerdict(ctx, g.batch.ID, raw)
	return err
}
