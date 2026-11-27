// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	pkgconfig "github.com/reliant-labs/reliant/pkg/config"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

func TestDispatchGateMapsTheGateRunner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var got gaterun.GateRequest
	d := &gaterun.MockDispatcher{DispatchFunc: func(_ context.Context, req gaterun.GateRequest) (gaterun.GateResult, error) {
		got = req
		return gaterun.GateResult{
			Class: gaterun.ClassFail, Isolation: gaterun.IsolationSysbox, Machine: "d1", ReasonCode: gaterun.ReasonStage,
			FailingTests: []string{"TestX"},
			Stages:       map[config.StageName]gaterun.StageResult{config.StageTest: {Name: config.StageTest, Verdict: gaterun.StageFail, Exit: 1}},
		}, nil
	}}
	res, err := mergequeue.DispatchGate{Dispatcher: d}.Run(ctx, mergequeue.GateRequest{
		RepoID: "r", BatchID: "b", Attempt: 2, CandidateRef: "reliant-queue/b", CandidateSHA: "abc", BaseSHA: "def", Shadow: true,
		Gate: config.ResolvedGate{AllowUnisolated: true}, PreferMachine: "builder-1",
	})
	if err != nil || got.RepoID != "r" || got.Attempt != 2 || got.CandidateRef != "reliant-queue/b" || got.BaseSHA != "def" || !got.Shadow || !got.AllowUnisolated ||
		got.PreferMachine != "builder-1" {
		t.Fatalf("request %+v, err %v", got, err)
	}
	var stages map[string]any
	if res.Class != mergequeue.GateFail || res.Isolation != "sysbox" || res.Machine != "d1" || res.FailingTests[0] != "TestX" ||
		json.Unmarshal(res.Stages, &stages) != nil || stages["test"] == nil {
		t.Fatalf("result %+v", res)
	}

	d.DispatchFunc = func(context.Context, gaterun.GateRequest) (gaterun.GateResult, error) {
		return gaterun.GateResult{}, gaterun.ErrGatePoolBusy
	}
	res, err = mergequeue.DispatchGate{Dispatcher: d}.Run(ctx, mergequeue.GateRequest{RepoID: "r", BatchID: "b", Attempt: 1})
	if err != nil || res.Class != mergequeue.GateUnproven || res.ReasonCode != gaterun.ReasonPoolBusy {
		t.Fatalf("a busy pool is unproven: %+v %v", res, err)
	}
}

func TestGateReposReadsTheEnrolment(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	g, err := mergequeue.GateRepos{Store: f.db}.GateRepo(f.ctx, f.repo.ID)
	if err != nil || g.Repository != "acme/widgets" || g.ProjectID != testProject || g.OwnerUserID != testUser || g.Mode != core.PipelineRepoModeShadow {
		t.Fatalf("gate repo %+v %v", g, err)
	}
	if _, err := (mergequeue.GateRepos{Store: f.db}).GateRepo(f.ctx, "nope"); err == nil {
		t.Fatal("an unknown repository is an error")
	}
}

func TestOnModeLandsAnUnisolatedPassOnlyWhenTheRepoOptsIn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	f.setConfig(`{"version": 1, "gate": {"run": "task gate", "allow_unisolated": true}}`)
	f.queue("a", 50, nil)
	f.gate.judge = func(req mergequeue.GateRequest, _ []string, _ int) mergequeue.GateResult {
		if !req.Gate.AllowUnisolated {
			t.Errorf("the gate request carries the opt-in")
		}
		r := pass()
		r.Isolation = "runc"
		return r
	}
	rep := f.tick(f.scheduler())
	if rep.Landing == "" || len(f.land.lands) != 1 {
		t.Fatalf("an opted-in runc pass lands: %+v", rep)
	}
	b := f.batch(rep.Landing)
	v := f.verdict(rep.Landing)
	if b.IsolationBackend != core.QueueIsolationRunc || !v.Landable || !v.UnisolatedAccepted {
		t.Fatalf("batch %+v verdict %+v", b, v)
	}
}

// lyingBuilder reports a candidate pushed somewhere the queue never asked.
type lyingBuilder struct{ localBuilder }

func (b lyingBuilder) Build(ctx context.Context, repo *core.PipelineRepo, req buildjob.BuildRequest) (buildjob.BuildResult, error) {
	res, err := b.localBuilder.Build(ctx, repo, req)
	res.Ref = "refs/heads/main"
	return res, err
}

// The build runs on a user's daemon, so the queue checks its report against
// the request before gating or recording anything of it.
func TestTheQueueRejectsABuildReportOutsideItsRefs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	lie := lyingBuilder{f.wd}
	locks, err := dblock.New(dblock.Deps{Logger: slog.Default(), Config: &pkgconfig.Config{}, Sessions: f.db.SessionPool()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := mergequeue.New(mergequeue.Deps{Store: f.db, Leader: mergequeue.AdvisoryLeader{Locks: locks}, Refs: f.wd, Builder: lie, Gate: f.gate, Authorizer: f.auth})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Tick(f.ctx, f.repo.ID)
	if err == nil || !strings.Contains(err.Error(), "outside the queue's refs") || rep.Landing != "" {
		t.Fatalf("report %+v, err %v", rep, err)
	}
	if b := f.batch(rep.Batches[0]); b.State != core.QueueBatchAbandoned {
		t.Fatalf("the batch is %s, not abandoned", b.State)
	}
}

func TestNoBuilderRefusesEveryJob(t *testing.T) {
	t.Parallel()
	if _, err := (mergequeue.NoBuilder{}).Build(context.Background(), nil, buildjob.BuildRequest{}); !errors.Is(err, mergequeue.ErrNoBuilder) {
		t.Fatalf("build: %v", err)
	}
	if _, err := (mergequeue.NoBuilder{}).ChangedPaths(context.Background(), nil, buildjob.PathsRequest{}); !errors.Is(err, mergequeue.ErrNoBuilder) {
		t.Fatalf("paths: %v", err)
	}
}
