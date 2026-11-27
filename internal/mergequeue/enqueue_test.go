// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

func (f *fixture) enqueue(p *testPR, params map[string]any) map[string]any {
	f.t.Helper()
	if params == nil {
		params = map[string]any{"record_id": p.recordID}
	}
	h := mergequeue.EnqueueHandler{Store: f.db, Refs: f.wd, Builder: f.wd, Authorizer: f.auth}
	res, err := h.Handle(f.ctx, &serveraction.Request{
		Name: mergequeue.EnqueueAction, Params: params,
		Principal: serveraction.Principal{UserID: testUser, OrgID: testOrg, ProjectID: testProject},
	})
	if err != nil {
		f.t.Fatalf("enqueue %s: %v", p.branch, err)
	}
	return res.Data
}

func TestEnqueueRecomputesAdmissionAndIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	a := f.open("a", 70, nil)

	first := f.enqueue(a, nil)
	if first["queued"] != true || first["head"] != a.head {
		t.Fatalf("enqueue: %v", first)
	}
	again := f.enqueue(a, nil)
	if again["queued"] != true || again["entry_id"] != first["entry_id"] {
		t.Fatalf("a second enqueue is the same entry: %v %v", first, again)
	}
	entries, err := f.db.ListAdmissibleQueueEntries(f.ctx, f.repo.ID, time.Now(), 0)
	if err != nil || len(entries) != 1 || entries[0].Priority != 70 {
		t.Fatalf("one entry, at the PR's priority: %+v %v", entries, err)
	}
	if res := f.enqueue(a, map[string]any{"record_id": a.recordID, "head": "beef"}); res["queued"] != false || res["code"] != mergequeue.RefuseHeadMoved {
		t.Fatalf("a stale head: %v", res)
	}
}

func TestEnqueueRefusesWhatIsNotAdmissible(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.setConfig(`{"version": 1, "gate": "task gate",
		"owner_approval": [{"kind": "approve_breaking_proto", "paths": ["proto/**"]}],
		"check": {"checks": ["lint"]}}`)

	// No pass verdict on the head.
	nv := f.open("a", 50, nil)
	if _, err := f.raw.ExecContext(f.ctx, `DELETE FROM pr_head_verdicts WHERE record_id = $1`, nv.recordID); err != nil {
		t.Fatal(err)
	}
	if res := f.enqueue(nv, nil); res["code"] != mergequeue.RefuseNoPass {
		t.Fatalf("no verdict: %v", res)
	}

	// A proto change needs the owner's decision, and the check tier's lint.
	proto := f.open("b", 50, map[string]string{"proto/api.proto": "syntax = \"proto3\";\n"})
	if res := f.enqueue(proto, nil); res["code"] != mergequeue.RefuseDecisions {
		t.Fatalf("decision required: %v", res)
	}
	if _, err := f.raw.ExecContext(f.ctx, `INSERT INTO pipeline_decisions (id, org_id, project_id, subject_kind, subject_id, head_sha,
		kind, state, reason, decided_by_user_id, decided_via, decided_at) VALUES ('ok-b', $1, $2, 'pr_head', $3, $4,
		'approve_breaking_proto', 'approved', 'fine', $5, 'web', now())`, testOrg, testProject, proto.recordID, proto.head, testUser); err != nil {
		t.Fatal(err)
	}
	if res := f.enqueue(proto, nil); res["code"] != mergequeue.RefuseCheckTier {
		t.Fatalf("check tier: %v", res)
	}
	if _, err := f.raw.ExecContext(f.ctx, `INSERT INTO pr_record_checks (id, provider, repository_id, head_sha, name, app_slug,
		check_run_id, status, conclusion, completed_at, observed_at) VALUES ('chk-1', 'github', 'rid-1', $1, 'lint', 'actions', 1,
		'completed', 'success', now(), now())`, proto.head); err != nil {
		t.Fatal(err)
	}
	if res := f.enqueue(proto, nil); res["queued"] != true {
		t.Fatalf("admissible now: %v", res)
	}

	// Off: nothing is queued.
	r, _ := f.db.GetPipelineRepo(f.ctx, f.repo.ID)
	if _, err := f.db.SetPipelineRepoMode(f.ctx, r.ID, r.Version, core.PipelineRepoModeOff); err != nil {
		t.Fatal(err)
	}
	other := f.open("c", 50, nil)
	if res := f.enqueue(other, nil); res["code"] != mergequeue.RefuseModeOff {
		t.Fatalf("mode off: %v", res)
	}
}
