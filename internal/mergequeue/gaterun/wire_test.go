// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// The dispatcher's wire, without a daemon. The dispatcher against daemons
// running the real gate host is tested in package gatehost.

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeRepos core.PipelineRepoMode

func (m fakeRepos) GateRepo(_ context.Context, id string) (GateRepo, error) {
	if id != "r1" {
		return GateRepo{}, errors.New("not enrolled")
	}
	return GateRepo{Repository: "acme/widgets", ProjectID: "p1", OwnerUserID: "u1", Mode: core.PipelineRepoMode(m)}, nil
}

const repoOn = fakeRepos(core.PipelineRepoModeOn)

func gateRequest() GateRequest {
	return GateRequest{
		RepoID: "r1", BatchID: "B1", Attempt: 1,
		CandidateRef: "reliant-queue/b1", CandidateSHA: testSHA,
		Gate: config.ResolvedGate{Runner: config.RunnerMachine, Pool: "gate", Timeout: time.Minute, Stages: []config.ResolvedStage{
			{Name: config.StageLint, State: config.StageOff},
			{Name: config.StageTest, State: config.StageOn, Kind: config.StageCommand, Command: "true"},
			{Name: config.StageBuild, State: config.StageUnavailable, Reason: "nothing to build"},
			{Name: config.StageSBOMScan, State: config.StageOff},
		}},
	}
}

func TestStartRequestCarriesTheWarmCacheOptInAndTheBase(t *testing.T) {
	repo, _ := repoOn.GateRepo(context.Background(), "r1")
	shadowRepo, _ := fakeRepos(core.PipelineRepoModeShadow).GateRepo(context.Background(), "r1")
	base := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name       string
		repo       GateRepo
		shadow     bool
		unisolated bool
		wantRunc   bool
		wantWarm   bool
	}{
		{name: "on, no opt-in", repo: repo},
		{name: "shadow gate of a shadow repository", repo: shadowRepo, shadow: true, wantRunc: true},
		{name: "on, opted in", repo: repo, unisolated: true, wantRunc: true, wantWarm: true},
		{name: "shadow, opted in", repo: shadowRepo, shadow: true, unisolated: true, wantRunc: true, wantWarm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := gateRequest()
			req.Shadow, req.AllowUnisolated, req.BaseSHA = tc.shadow, tc.unisolated, base
			s, err := startRequest(tc.repo, req)
			if err != nil {
				t.Fatal(err)
			}
			if s.AllowNotIsolated != tc.wantRunc || s.WarmCache != tc.wantWarm {
				t.Fatalf("AllowNotIsolated=%v WarmCache=%v, want %v %v", s.AllowNotIsolated, s.WarmCache, tc.wantRunc, tc.wantWarm)
			}
			if s.BaseSHA != base {
				t.Fatalf("base %q, want the batch's %q", s.BaseSHA, base)
			}
		})
	}
}

func TestPlanFromGate(t *testing.T) {
	p, err := PlanFromGate(config.Defaults().Gate)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 4 || p[3].Kind != config.StageBuiltinScan || p[3].State != config.StageOn {
		t.Fatalf("defaults: %+v", p)
	}
	if err := validatePlan(p); err != nil {
		t.Fatalf("the defaults' plan is invalid: %v", err)
	}
	// An on stage with nothing to run becomes unavailable, never a pass.
	g := config.ResolvedGate{Stages: []config.ResolvedStage{{Name: config.StageLint, State: config.StageOn, Kind: config.StageCommand}}}
	p, _ = PlanFromGate(g)
	if p[0].State != config.StageUnavailable {
		t.Fatalf("an empty command: %+v", p[0])
	}
	if got := GateID("Batch/ID 7", 2); got != "gate-batch-id-7-2" {
		t.Fatalf("gate id %q", got)
	}
	if got := GateID(strings.Repeat("x", 80), 12); len(got) != 63 || !strings.HasSuffix(got, "-12") {
		t.Fatalf("long gate id %q", got)
	}
}

func TestPreferMemberMovesTheBuilderFirst(t *testing.T) {
	ms := []machinepool.Member{{DaemonID: "a"}, {DaemonID: "b"}, {DaemonID: "c"}}
	ids := func(ms []machinepool.Member) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.DaemonID)
		}
		return strings.Join(s, ",")
	}
	for prefer, want := range map[string]string{"": "a,b,c", "a": "a,b,c", "c": "c,a,b", "b": "b,a,c", "gone": "a,b,c"} {
		if got := ids(PreferMember(ms, prefer)); got != want {
			t.Errorf("prefer %q: %s, want %s", prefer, got, want)
		}
	}
	if ids(ms) != "a,b,c" {
		t.Fatal("PreferMember changed its input")
	}
}
