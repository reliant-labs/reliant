package practions_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// pr.next lists a head's files on the gate pool against its merge base with
// the PR's base branch as GitHub has it now, never a stale base.
func TestNextListsFilesAgainstTheBaseBranchTip(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.set(h.number, "head-1", "docs/guide.md@b1")
	if got := h.must(serveraction.PRNext, call{node: "decide"}); got["next"] != practions.NextEnqueue {
		t.Fatalf("a docs-only head listed by the gate pool passes with no model: %v", got)
	}
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	if !slices.Equal(h.files.bases, []string{baseTip + "-main"}) {
		t.Fatalf("listed against %v, not the base branch's tip", h.files.bases)
	}
}

// With no gate pool daemon to list the files, a head that would have passed
// with no model is judged high risk and goes to a model: nothing is let
// through on a guess, and the diff is never read on the worker instead.
func TestNextWithNoGatePoolDaemonSendsTheHeadToAModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.set(h.number, "head-1", "docs/guide.md@b1", "README.md@b2")
	h.files.unavailable.Store(true)

	got := h.must(serveraction.PRNext, call{node: "decide"})
	if got["next"] != practions.NextReview || got["risk_tier"] != "high" ||
		got["reason"] != "changed files unavailable without a gate pool daemon" {
		t.Fatalf("a head with unknown files goes to a model, high risk: %v", got)
	}
	if v, err := h.env.Repo.LatestPRHeadVerdict(h.ctx, h.record, "head-1"); err != nil || v != nil {
		t.Fatalf("no structural verdict is written: %+v %v", v, err)
	}
	if r := h.row(); r.State != core.PRPipelineReviewing {
		t.Fatalf("reviewing: %+v", r)
	}
}

// A worker with no Files at all (no router) reviews every head by model.
func TestNextWithNoFilesSendsTheHeadToAModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.set(h.number, "head-1", "docs/guide.md@b1")
	svc, err := practions.New(practions.Deps{Store: h.env.Repo, GitHub: h.gh, Inbox: h.inbox, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	got := h.must(serveraction.PRNext, call{node: "decide"})
	if got["next"] != practions.NextReview || got["risk_tier"] != "high" {
		t.Fatalf("no Files: a model reviews: %v", got)
	}
}

// When the files were never listed, an owner decision gated on paths is
// required of the model's pass anyway (fail closed): an unknown diff could
// touch the gated paths.
func TestUnknownFilesRequireEveryPathGatedDecision(t *testing.T) {
	t.Parallel()
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.config(`{"version": 1, "gate": "task test", "owner_approval": [{"kind": "approve_breaking_proto", "paths": ["proto/**"]}]}`)
	h.files.unavailable.Store(true)
	h.must(serveraction.PRNext, call{node: "decide"})
	out := h.must(serveraction.PRVerdict, verdictCall(review("pass", "session-r1")))
	if out["next"] != practions.NextEscalate || !strings.Contains(fmt.Sprint(out["reason"]), "approve_breaking_proto") {
		t.Fatalf("a pass over unknown files needs every path-gated decision: %v", out)
	}
}

func TestFilesFromStats(t *testing.T) {
	t.Parallel()
	blob := strings.Repeat("b", 40)
	got := practions.FilesFromStats(buildjob.PathsResult{Stats: true, Files: []buildjob.FileStat{
		{Path: "a.go", Status: buildjob.FileModified, Additions: 3, Deletions: 1, BlobSHA: blob},
		{Path: "new/b.go", PreviousPath: "old/b.go", Status: buildjob.FileRenamed, Additions: 1, BlobSHA: blob},
	}})
	if got.Diff.ChangedFiles != 2 || got.Diff.Additions != 4 || got.Diff.Deletions != 1 ||
		got.Diff.Files[1].PreviousPath != "old/b.go" || got.Diff.Files[1].Status != prpolicy.FileRenamed || got.Blobs["new/b.go"] != blob {
		t.Fatalf("converted: %+v", got)
	}
	cut := practions.FilesFromStats(buildjob.PathsResult{Stats: true, FilesTruncated: true, Files: []buildjob.FileStat{
		{Path: "a.go", Status: buildjob.FileModified, BlobSHA: blob},
	}})
	if cut.Diff.ChangedFiles <= len(cut.Diff.Files) {
		t.Fatalf("a truncated list says more changed than were listed: %+v", cut.Diff)
	}
}
