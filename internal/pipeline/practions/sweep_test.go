// Copyright (c) 2025 Reliant Labs
package practions_test

import (
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// setRow moves a PR's row to what edit makes of it, then backdates its last
// change by age.
func (h *harness) setRow(id string, age time.Duration, edit func(*core.PRPipeline)) {
	h.t.Helper()
	row, err := h.env.Repo.GetPRPipeline(h.ctx, id)
	if err != nil || row == nil {
		h.t.Fatalf("row %s: %v", id, err)
	}
	to := *row
	edit(&to)
	if _, err := h.env.Repo.TransitionPRPipeline(h.ctx, id, row.Version, to); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.env.SQL.Exec(`UPDATE pr_pipeline SET updated_at = $2 WHERE record_id = $1`, id, h.now.Add(-age)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) rowOf(id string) *core.PRPipeline {
	h.t.Helper()
	r, err := h.env.Repo.GetPRPipeline(h.ctx, id)
	if err != nil || r == nil {
		h.t.Fatalf("row %s: %v", id, err)
	}
	return r
}

// findingsByPR is the sweep's report keyed by PR.
func findingsByPR(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	by := map[string]map[string]any{}
	for _, raw := range out["findings"].([]any) {
		f := raw.(map[string]any)
		by[f["pr"].(string)] = f
	}
	return by
}

func TestSweepActsOncePerPRThatFellThroughTheCracks(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOn)
	// #7 (h.record) carries the head an older open PR, #3, already has: superseded.
	h.addPR(3, "head-1")
	// #8: nothing due and nothing for 30 hours.
	stale := h.addPR(8, "head-8")
	h.setRow(stale, 30*time.Hour, func(r *core.PRPipeline) { r.State = core.PRPipelineChangesRequested })
	// #9: waits for a person, but its re-page was lost.
	waiting := h.addPR(9, "head-9")
	h.setRow(waiting, 5*time.Hour, func(r *core.PRPipeline) {
		r.State, r.NeedsPersonReason, r.NeedsPersonSince = core.PRPipelineNeedsPerson, "owner decision", &h.now
	})
	// #10: a draft untouched for 15 days.
	draft := h.addPR(10, "head-10")
	h.setRow(draft, 15*24*time.Hour, func(r *core.PRPipeline) { r.State = core.PRPipelineDraft })
	// #11: the queue ejected its head and nothing picked it up.
	ejected := h.addPR(11, "head-11")
	h.setRow(ejected, time.Hour, func(r *core.PRPipeline) { r.State = core.PRPipelineQueued })
	entry, _, err := h.env.Repo.EnqueueQueueEntry(h.ctx, core.QueueEntryNew{OrgID: testOrg, ProjectID: h.repo.ProjectID,
		RepoID: h.repo.ID, RecordID: ejected, HeadSHA: "head-11"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Repo.EjectQueueEntry(h.ctx, entry.ID, core.QueueEjectGateRed); err != nil {
		t.Fatal(err)
	}
	// #12: fresh, with a decision due later: left alone.
	fresh := h.addPR(12, "head-12")
	later := h.now.Add(time.Hour)
	h.setRow(fresh, time.Minute, func(r *core.PRPipeline) { r.NextAction, r.NextActionAt = practions.NextDecide, &later })

	out := h.must(serveraction.SweepPR, call{node: "sweep"})
	by := findingsByPR(t, out)
	want := map[string][2]string{
		"acme/app#7":  {practions.CheckSuperseded, practions.SweepClose},
		"acme/app#8":  {practions.CheckStalePR, practions.SweepFinish},
		"acme/app#9":  {practions.CheckStaleNeedsPerson, practions.SweepAsk},
		"acme/app#10": {practions.CheckDraftAbandoned, practions.SweepAsk},
		"acme/app#11": {practions.CheckStuckInQueue, practions.SweepFinish},
	}
	if len(by) != len(want) {
		t.Fatalf("want %d findings, got %v", len(want), by)
	}
	for pr, w := range want {
		f := by[pr]
		if f == nil || f["check"] != w[0] || f["action"] != w[1] || f["error"] != nil {
			t.Fatalf("%s: want %s -> %s, got %v", pr, w[0], w[1], f)
		}
	}
	if counts := out["counts"].(map[string]any); counts["finish"] != float64(2) && counts["finish"] != 2 {
		t.Fatalf("counts: %v", counts)
	}

	// Each action happened, once.
	if !slices.Contains(h.gh.closed, 7) || len(h.gh.closed) != 1 {
		t.Fatalf("only #7 closes: %v", h.gh.closed)
	}
	for _, id := range []string{stale, ejected} {
		if r := h.rowOf(id); r.NextAction != practions.NextDecide || r.NextActionAt == nil || r.NextActionAt.After(h.now) {
			t.Fatalf("%s is woken now: %+v", id, r)
		}
	}
	for _, id := range []string{waiting, draft} {
		if r := h.rowOf(id); r.State != core.PRPipelineNeedsPerson {
			t.Fatalf("%s asks a person: %+v", id, r)
		}
	}
	if len(h.inbox.items) != 2 {
		t.Fatalf("two pages: %v", h.inbox.items)
	}
	if r := h.rowOf(fresh); r.NextActionAt == nil || !r.NextActionAt.Equal(later) {
		t.Fatalf("a PR with a decision due is left alone: %+v", r)
	}

	// The same sweep retried repeats nothing.
	h.must(serveraction.SweepPR, call{node: "sweep", key: "exec-retry"})
	if len(h.gh.closed) != 1 {
		t.Fatalf("a retry closes nothing more: %v", h.gh.closed)
	}
}

// A PR the sweep closed and a person reopened is never closed again for the
// same evidence, by the sweep or by pr.next.
func TestSweepNeverReclosesAPRAPersonReopened(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOn)
	h.addPR(3, "head-1")
	h.must(serveraction.SweepPR, call{node: "sweep"})
	if len(h.gh.closed) != 1 {
		t.Fatalf("#7 is closed as superseded: %v", h.gh.closed)
	}

	// A person reopens it; the projector starts its row over.
	h.gh.mu.Lock()
	pr := h.gh.prs[7]
	pr.State = "open"
	h.gh.prs[7] = pr
	h.gh.mu.Unlock()
	h.setRow(h.record, 30*time.Hour, func(r *core.PRPipeline) { r.State = core.PRPipelineAwaitingReview })

	out := h.must(serveraction.SweepPR, call{node: "sweep"})
	if f := findingsByPR(t, out)["acme/app#7"]; f != nil && f["action"] == practions.SweepClose {
		t.Fatalf("a reopened PR is not closed again for the same evidence: %v", f)
	}
	if len(h.gh.closed) != 1 {
		t.Fatalf("no second close: %v", h.gh.closed)
	}
	if next := h.must(serveraction.PRNext, call{node: "decide"}); next["next"] == practions.NextClose {
		t.Fatalf("pr.next does not close it again either: %v", next)
	}
	// A direct pr.close for the same evidence keeps it open too.
	res := h.must(serveraction.PRClose, call{params: map[string]any{"evidence": map[string]any{"kind": "duplicate", "by": float64(3)}}})
	if res["closed"] != false || len(h.gh.closed) != 1 {
		t.Fatalf("pr.close keeps a person's reopen: %v %v", res, h.gh.closed)
	}
}

func TestSweepInShadowWritesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.addPR(3, "head-1")
	out := h.must(serveraction.SweepPR, call{node: "sweep"})
	f := findingsByPR(t, out)["acme/app#7"]
	if f == nil || f["action"] != practions.SweepClose || f["shadow"] != true {
		t.Fatalf("the close is reported, in shadow: %v", f)
	}
	writes, _ := f["writes"].([]any)
	if len(writes) != 2 || writes[0].(map[string]any)["suppressed"] != true {
		t.Fatalf("its writes are recorded, suppressed: %v", writes)
	}
	if len(h.gh.closed) != 0 || len(h.gh.comments) != 0 {
		t.Fatalf("shadow writes nothing to GitHub: %v %v", h.gh.closed, h.gh.comments)
	}
}

func TestSweepRefusesAnUnknownCheckAndStaysInItsProject(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeOn)
	_, err := h.do(serveraction.SweepPR, call{params: map[string]any{"checks": []any{"nope"}}})
	refused(t, err, serveraction.CodeInvalidArgument)

	h.addPR(3, "head-1")
	other := serveraction.Principal{UserID: "u", OrgID: testOrg, ProjectID: "project-other"}
	out := h.must(serveraction.SweepPR, call{principal: &other})
	if out["looked"] != 0 || len(h.gh.closed) != 0 {
		t.Fatalf("another project's sweep sees none of these PRs: %v", out)
	}
}

func TestSweepSpecNamesEveryCheck(t *testing.T) {
	t.Parallel()
	spec, err := serveraction.Builtin().Resolve(serveraction.SweepPR)
	if err != nil {
		t.Fatal(err)
	}
	enum := spec.Params["properties"].(map[string]any)["checks"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	var names []string
	for _, v := range enum {
		names = append(names, v.(string))
	}
	if !slices.Equal(names, practions.SweepChecks) {
		t.Fatalf("the spec's checks %v must be practions.SweepChecks %v", names, practions.SweepChecks)
	}
}
