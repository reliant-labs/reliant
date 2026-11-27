// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

type fakePools struct {
	pool *core.MachinePool
	rows []core.MachinePoolMemberRow
}

func (p *fakePools) GetMachinePoolByName(_ context.Context, userID, name string) (*core.MachinePool, error) {
	if p.pool == nil || p.pool.UserID != userID || p.pool.Name != name {
		return nil, core.ErrMachinePoolNotFound
	}
	return p.pool, nil
}

func (p *fakePools) GetMachinePoolForUser(context.Context, string, string) (*core.MachinePool, error) {
	return p.pool, nil
}

func (p *fakePools) ListMachinePoolMembers(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
	return p.rows, nil
}

// fakeDaemons is the daemon command path: each daemon id has its own Jobs,
// answered by the same Handle the daemon's command handler calls.
type fakeDaemons struct {
	mu      sync.Mutex
	daemons map[string]*Jobs
	calls   []string
	// fail makes a command to a daemon fail on the transport.
	fail map[string]error
}

func (d *fakeDaemons) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	d.mu.Lock()
	d.calls = append(d.calls, daemonID+" "+command)
	j, err := d.daemons[daemonID], d.fail[daemonID]
	d.mu.Unlock()
	if userID != "u1" {
		return nil, fmt.Errorf("command for %s sent as %s", daemonID, userID)
	}
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, errors.New("no responders")
	}
	return j.Handle(ctx, command, payload)
}

func (d *fakeDaemons) sent(command string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if strings.HasSuffix(c, " "+command) {
			out = append(out, strings.TrimSuffix(c, " "+command))
		}
	}
	return out
}

// fakeRepos is pipeline_repos: one enrolled repository, r1, in mode.
type fakeRepos core.PipelineRepoMode

func (m fakeRepos) GateRepo(_ context.Context, id string) (gaterun.GateRepo, error) {
	if id != "r1" {
		return gaterun.GateRepo{}, errors.New("not enrolled")
	}
	return gaterun.GateRepo{Repository: "acme/widgets", ProjectID: "p1", OwnerUserID: "u1", Mode: core.PipelineRepoMode(m)}, nil
}

const repoOn = fakeRepos(core.PipelineRepoModeOn)

func member(id, name string, phase string) core.MachinePoolMemberRow {
	catalog := "[]"
	return core.MachinePoolMemberRow{DaemonID: id, Name: name, Phase: phase, Installed: true, Verified: true,
		WorkflowsJSON: &catalog, PresetsJSON: &catalog}
}

// gateMachine is a Jobs whose Runner has the given backends over a tree.
func gateMachine(t *testing.T, isolated Backend, runc Backend) *Jobs {
	t.Helper()
	r := newRunner(t, isolated, nil)
	if isolated == nil {
		r.Isolated = nil
	}
	r.NotIsolated = runc
	return &Jobs{Runner: r}
}

// releaseAtEnd lets f's blocked gate go when the test ends and waits for it
// to finish, before the test's temporary directories are removed (cleanups
// run last-registered first, and this is registered after them).
func releaseAtEnd(t *testing.T, f *fakeBackend, jobs *Jobs, gateID string) {
	t.Cleanup(func() {
		close(f.block)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if st := jobs.Status(gateID); st.State != gaterun.JobRunning {
				return
			}
		}
		t.Errorf("gate %s did not finish", gateID)
	})
}

func gateRequest() gaterun.GateRequest {
	return gaterun.GateRequest{
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

func newDispatcher(pools *fakePools, daemons *fakeDaemons, repos gaterun.RepoReader) gaterun.Dispatcher {
	if repos == nil {
		repos = repoOn
	}
	return gaterun.NewDispatcher(gaterun.Deps{Repos: repos, Pools: pools, Daemons: daemons, PollInterval: time.Millisecond})
}

func gatePool(rows ...core.MachinePoolMemberRow) *fakePools {
	return &fakePools{pool: &core.MachinePool{ID: "pool1", UserID: "u1", Name: "gate", MaxRunsPerMember: 1}, rows: rows}
}

func TestDispatchRunsTheGateOnAConnectedMember(t *testing.T) {
	daemons := &fakeDaemons{daemons: map[string]*Jobs{
		"d-a": gateMachine(t, newFake(t, gaterun.IsolationSysbox), nil),
		"d-b": gateMachine(t, newFake(t, gaterun.IsolationSysbox), nil),
	}}
	pools := gatePool(
		member("d-z", "aaa-asleep", "suspended"),
		member("d-b", "bbb", "connected"),
		member("d-a", "ccc", "connected"),
		member("d-x", "000-not-installed", "connected"),
	)
	pools.rows[3].Installed = false
	g, err := newDispatcher(pools, daemons, nil).Dispatch(context.Background(), gateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if g.Machine != "d-b" || g.Class != gaterun.ClassPass || !g.Landable() || g.GateID != "gate-b1-1" {
		t.Fatalf("result %+v", g)
	}
	if got := daemons.sent(gaterun.CommandStart); len(got) != 1 || got[0] != "d-b" {
		t.Fatalf("started on %v, want the first connected eligible member by name", got)
	}
	if got := states(g); got != "lint=off test=pass build=unavailable sbom_scan=off" {
		t.Fatalf("stages %s", got)
	}
}

func TestDispatchSkipsBusyAndBrokenMembers(t *testing.T) {
	busy := newFake(t, gaterun.IsolationSysbox)
	busy.block = make(chan struct{})
	busyJobs := gateMachine(t, busy, nil)
	releaseAtEnd(t, busy, busyJobs, "gate-other-1")
	other := startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"), offStage(config.StageBuild), offStage(config.StageSBOMScan))
	other.GateID = "gate-other-1"
	if r := busyJobs.Start(context.Background(), other); r.State != gaterun.JobRunning {
		t.Fatal(r)
	}
	daemons := &fakeDaemons{
		daemons: map[string]*Jobs{"d-a": busyJobs, "d-c": gateMachine(t, newFake(t, gaterun.IsolationSysbox), nil)},
		fail:    map[string]error{"d-b": errors.New("connection reset")},
	}
	pools := gatePool(member("d-a", "a", "connected"), member("d-b", "b", "connected"), member("d-c", "c", "connected"))
	g, err := newDispatcher(pools, daemons, nil).Dispatch(context.Background(), gateRequest())
	if err != nil || g.Machine != "d-c" {
		t.Fatalf("%+v %v", g, err)
	}
	if got := daemons.sent(gaterun.CommandCancel); len(got) != 1 || got[0] != "d-b" {
		t.Fatalf("a start whose reply was lost is canceled on its member: %v", got)
	}
}

func TestDispatchRefusesWithoutAnIsolatedMachine(t *testing.T) {
	noSysbox := &fakeBackend{iso: gaterun.IsolationSysbox, unavailable: "sysbox-runc is not installed"}
	runc := newFake(t, gaterun.IsolationRunc)
	daemons := &fakeDaemons{daemons: map[string]*Jobs{"d-a": gateMachine(t, noSysbox, runc)}}
	pools := gatePool(member("d-a", "a", "connected"))

	_, err := newDispatcher(pools, daemons, repoOn).Dispatch(context.Background(), gateRequest())
	if !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) || !strings.Contains(err.Error(), "sysbox-runc is not installed") {
		t.Fatalf("a non-shadow gate: %v", err)
	}
	if r := gaterun.ResultFromError("gate-b1-1", err); r.Class != gaterun.ClassUnproven || r.ReasonCode != gaterun.ReasonNoIsolatedMachine || r.Landable() {
		t.Fatalf("ResultFromError: %+v", r)
	}

	// A shadow request for a repository that is on is still refused: both
	// must say shadow.
	req := gateRequest()
	req.Shadow = true
	if _, err := newDispatcher(pools, daemons, repoOn).Dispatch(context.Background(), req); !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) {
		t.Fatalf("shadow request, repo on: %v", err)
	}
	if len(runc.specs) != 0 {
		t.Fatal("runc ran a gate it was not allowed")
	}

	// Shadow and shadow: runc runs it, and the result can never land.
	g, err := newDispatcher(pools, daemons, fakeRepos(core.PipelineRepoModeShadow)).Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if g.Isolation != gaterun.IsolationRunc || g.Class != gaterun.ClassPass || g.Landable() {
		t.Fatalf("shadow gate on runc: %+v", g)
	}
	if last := runc.specs[len(runc.specs)-1]; last.CacheVolume != "" {
		t.Fatalf("a shadow gate of a repository that did not opt in got the warm cache %q", last.CacheVolume)
	}

	// A repository that is on and opted in to unisolated gates runs on
	// runc too; marking the batch unisolated_accepted is the caller's.
	opted := gateRequest()
	opted.AllowUnisolated = true
	opted.Attempt = 2 // a new gate id: the member answers gate-b1-1 from its result
	g, err = newDispatcher(pools, daemons, repoOn).Dispatch(context.Background(), opted)
	if err != nil {
		t.Fatal(err)
	}
	if g.Isolation != gaterun.IsolationRunc || g.Class != gaterun.ClassPass {
		t.Fatalf("opted-in gate on runc: %+v", g)
	}
	// The opt-in is also the warm cache's: the gate gets the repository's
	// cache volume.
	if last := runc.specs[len(runc.specs)-1]; last.CacheVolume != gaterun.CacheVolumeName("acme/widgets") {
		t.Fatalf("an opted-in gate's cache volume is %q, want %q", last.CacheVolume, gaterun.CacheVolumeName("acme/widgets"))
	}
}

func TestDispatchErrors(t *testing.T) {
	ctx := context.Background()
	daemons := &fakeDaemons{daemons: map[string]*Jobs{}}

	if _, err := newDispatcher(&fakePools{}, daemons, nil).Dispatch(ctx, gateRequest()); !errors.Is(err, gaterun.ErrNoGateMember) {
		t.Fatalf("no pool: %v", err)
	}
	if _, err := newDispatcher(gatePool(member("d-a", "a", "disconnected")), daemons, nil).Dispatch(ctx, gateRequest()); !errors.Is(err, gaterun.ErrNoGateMember) || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("no connected member: %v", err)
	}
	req := gateRequest()
	req.Gate.Runner = config.RunnerChecks
	if _, err := newDispatcher(gatePool(), daemons, nil).Dispatch(ctx, req); !errors.Is(err, gaterun.ErrInvalidRequest) {
		t.Fatalf("runner checks: %v", err)
	}
	req = gateRequest()
	req.CandidateSHA = "main"
	if _, err := newDispatcher(gatePool(), daemons, nil).Dispatch(ctx, req); !errors.Is(err, gaterun.ErrInvalidRequest) {
		t.Fatalf("not a sha: %v", err)
	}
	req = gateRequest()
	req.RepoID = "r2"
	if _, err := newDispatcher(gatePool(), daemons, nil).Dispatch(ctx, req); err == nil {
		t.Fatal("a gate of an unenrolled repo was dispatched")
	}
	if _, err := newDispatcher(gatePool(), daemons, fakeRepos(core.PipelineRepoModeOff)).Dispatch(ctx, gateRequest()); !errors.Is(err, gaterun.ErrInvalidRequest) {
		t.Fatalf("a repository that is off: %v", err)
	}
}

func TestDispatchReportsALostGate(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.block = make(chan struct{})
	jobs := gateMachine(t, f, nil)
	releaseAtEnd(t, f, jobs, "gate-b1-1")
	daemons := &fakeDaemons{daemons: map[string]*Jobs{"d-a": jobs}}
	pools := gatePool(member("d-a", "a", "connected"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := newDispatcher(pools, daemons, nil).Dispatch(ctx, gateRequest())
		errc <- err
	}()
	// The member restarts mid-gate: it no longer knows the gate id.
	for len(daemons.sent(gaterun.CommandStatus)) == 0 {
		time.Sleep(time.Millisecond)
	}
	daemons.mu.Lock()
	daemons.daemons["d-a"] = &Jobs{Runner: jobs.Runner}
	daemons.mu.Unlock()
	if err := <-errc; !errors.Is(err, gaterun.ErrGateLost) {
		t.Fatalf("restarted member: %v", err)
	}
}

func TestDispatchCancelsTheGateWhenItsCallerGoesAway(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.block = make(chan struct{})
	jobs := gateMachine(t, f, nil)
	releaseAtEnd(t, f, jobs, "gate-b1-1")
	daemons := &fakeDaemons{daemons: map[string]*Jobs{"d-a": jobs}}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := newDispatcher(gatePool(member("d-a", "a", "connected")), daemons, nil).Dispatch(ctx, gateRequest())
		errc <- err
	}()
	for len(daemons.sent(gaterun.CommandStatus)) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	err := <-errc
	if !errors.Is(err, context.Canceled) || gaterun.ResultFromError("g", err).ReasonCode != gaterun.ReasonCanceled {
		t.Fatalf("canceled dispatch: %v", err)
	}
	if got := daemons.sent(gaterun.CommandCancel); len(got) != 1 {
		t.Fatalf("cancel sent %v", got)
	}
}

func TestDispatchReportsFailingTestsAndInfraMatches(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, nil)
	jobs := &Jobs{Runner: r}
	daemons := &fakeDaemons{daemons: map[string]*Jobs{"d-a": jobs}}
	req := gateRequest()
	req.Gate.Stages[1].Command = `mkdir -p .reliant && echo '{"failing_tests":["TestA","TestB"]}' >.reliant/gate-result.json; echo "dial tcp 10.0.0.1:443: i/o timeout"; exit 1`
	req.Gate.InfraPatterns = []*regexp.Regexp{regexp.MustCompile(`i/o timeout`), regexp.MustCompile(`no space left`)}
	g, err := newDispatcher(gatePool(member("d-a", "a", "connected")), daemons, nil).Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if g.Class != gaterun.ClassFail || !slices.Equal(g.FailingTests, []string{"TestA", "TestB"}) || !slices.Equal(g.InfraMatches, []string{"i/o timeout"}) {
		t.Fatalf("class %s failing %v infra %v", g.Class, g.FailingTests, g.InfraMatches)
	}
}
