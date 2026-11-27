// Copyright (c) 2025 Reliant Labs
package jobrun_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/jobrun"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// handler is one fake daemon: its answer to a command.
type handler func(ctx context.Context, command string, payload []byte) ([]byte, error)

// fakeDaemons is the daemon command path: each daemon id answers through its
// own handler, as the daemon's command registry would.
type fakeDaemons struct {
	mu      sync.Mutex
	daemons map[string]handler
	calls   []string
}

func (d *fakeDaemons) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	d.mu.Lock()
	d.calls = append(d.calls, daemonID+" "+command)
	h := d.daemons[daemonID]
	d.mu.Unlock()
	if userID != "u1" {
		return nil, fmt.Errorf("command for %s sent as %s", daemonID, userID)
	}
	if h == nil {
		return nil, errors.New("no responders")
	}
	return h(ctx, command, payload)
}

func (d *fakeDaemons) sentTo(daemonID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if id, cmd, _ := strings.Cut(c, " "); id == daemonID {
			out = append(out, cmd)
		}
	}
	return out
}

// oldDaemon is a member whose Reliant predates pipeline jobs: its registry
// does not know the commands.
func oldDaemon(_ context.Context, command string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("this machine is running an older version of Reliant (v1.2.3) that doesn't support %q — update it to continue", command)
}

// answer is a daemon that answers every start with start and every status
// with status.
func answer(start buildjob.StartReply, status buildjob.StatusReply) handler {
	return func(_ context.Context, command string, _ []byte) ([]byte, error) {
		switch command {
		case buildjob.CommandStart:
			return json.Marshal(start)
		case buildjob.CommandStatus:
			return json.Marshal(status)
		}
		return []byte("{}"), nil
	}
}

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

func member(id, name string) core.MachinePoolMemberRow {
	catalog := "[]"
	return core.MachinePoolMemberRow{DaemonID: id, Name: name, Phase: "connected", Installed: true, Verified: true,
		WorkflowsJSON: &catalog, PresetsJSON: &catalog}
}

// gatePool is user u1's pool "gate" with members.
func gatePool(rows ...core.MachinePoolMemberRow) *fakePools {
	return &fakePools{pool: &core.MachinePool{ID: "pool1", UserID: "u1", Name: "gate", MaxRunsPerMember: 2}, rows: rows}
}

type clients struct{ gh ghport.Client }

func (c clients) Client(context.Context, *core.PipelineRepo) (ghport.Client, error) { return c.gh, nil }

type repos []*core.PipelineRepo

func (r repos) ListPipelineReposByRepository(_ context.Context, provider, repository string) ([]*core.PipelineRepo, error) {
	if provider != prrecords.ProviderGitHub {
		return nil, fmt.Errorf("provider %q", provider)
	}
	var out []*core.PipelineRepo
	for _, p := range r {
		if p.Repository == repository {
			out = append(out, p)
		}
	}
	return out, nil
}

func enrolled(mode core.PipelineRepoMode) *core.PipelineRepo {
	return &core.PipelineRepo{ID: "r1", ProjectID: "p1", Provider: "github", Repository: "acme/widgets", Mode: mode, CreatedByUserID: "u1"}
}

// world is a fake GitHub with one repository, whose main has two files, and
// a gate pool member running the real daemon-side job host over it.
type world struct {
	t       *testing.T
	g       *fakegithub.GitHub
	r       *fakegithub.Repo
	host    *candidate.Host
	daemons *fakeDaemons
	pools   *fakePools
}

func newWorld(t *testing.T) *world {
	t.Helper()
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	r.Commit("main", "base", map[string]string{"x.txt": "x\n", "y.txt": "y\n"})
	h := &candidate.Host{Root: filepath.Join(t.TempDir(), "jobs"), GitHub: g, RemoteURL: func(string) string { return r.URL() }}
	return &world{t: t, g: g, r: r, host: h,
		daemons: &fakeDaemons{daemons: map[string]handler{"d-b": h.Handle}},
		pools:   gatePool(member("d-b", "b")),
	}
}

func (w *world) deps() jobrun.Deps {
	return jobrun.Deps{Pools: w.pools, Daemons: w.daemons, GitHub: clients{w.g}, Repos: repos{enrolled(core.PipelineRepoModeOn)}, PollInterval: time.Millisecond}
}

func (w *world) builder() *jobrun.Builder {
	w.t.Helper()
	b, err := jobrun.NewBuilder(w.deps())
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

func (w *world) pr(branch string, files map[string]string) buildjob.Entry {
	w.r.Branch(branch, "main")
	head := w.r.Commit(branch, "work on "+branch, files)
	title := "Change " + branch
	n := w.r.OpenPR(fakegithub.PROptions{Title: title, Head: branch})
	return buildjob.Entry{Number: n, Head: head, Title: title}
}

func TestBuildRunsOnAPoolMemberAndIsVerifiedOnGitHub(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	// Member a runs a Reliant older than pipeline jobs: refused, and b,
	// the next by name, builds.
	w.daemons.daemons["d-a"] = oldDaemon
	w.pools.rows = append([]core.MachinePoolMemberRow{member("d-a", "a")}, w.pools.rows...)
	one := w.pr("one", map[string]string{"x.txt": "x1\n"})
	two := w.pr("two", map[string]string{"z.txt": "z\n"})
	m := w.r.Head("main")

	res, err := w.builder().Build(t.Context(), enrolled(core.PipelineRepoModeOn), buildjob.BuildRequest{
		Repository: "acme/widgets", BatchID: "b1", Base: m, Entries: []buildjob.Entry{one, two},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Machine != "d-b" || res.Ref != buildjob.QueueRef("b1") || len(res.Trees) != 2 || w.r.Head("reliant-queue/b1") != res.Commit {
		t.Fatalf("result: %+v", res)
	}
	if got := w.daemons.sentTo("d-a"); !slices.Equal(got, []string{buildjob.CommandStart}) {
		t.Fatalf("the old member was sent %v", got)
	}
}

func TestChangedPathsRunOnAPoolMember(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	e := w.pr("one", map[string]string{"x.txt": "x1\n", "a/b.txt": "b\n"})
	res, err := w.builder().ChangedPaths(t.Context(), enrolled(core.PipelineRepoModeShadow), buildjob.PathsRequest{
		Repository: "acme/widgets", Base: w.r.Head("main"), Head: e.Head,
	})
	if err != nil || !slices.Equal(res.Paths, []string{"a/b.txt", "x.txt"}) {
		t.Fatalf("ChangedPaths: %+v, %v", res, err)
	}
}

func TestJobsRunOnlyForARepositoryThatRunsTheQueue(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	b := w.builder()
	m := w.r.Head("main")
	req := buildjob.BuildRequest{Repository: "acme/widgets", BatchID: "b1", Base: m, Entries: []buildjob.Entry{{Number: 1, Head: m}}}
	if _, err := b.Build(t.Context(), enrolled(core.PipelineRepoModeOff), req); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a build for a repository that is off: %v", err)
	}
	if _, err := b.Build(t.Context(), enrolled(core.PipelineRepoModeShadow), req); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a landing build for a shadow repository: %v", err)
	}
	other := req
	other.Repository = "acme/other"
	if _, err := b.Build(t.Context(), enrolled(core.PipelineRepoModeOn), other); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a build of another repository: %v", err)
	}
	if _, err := b.ChangedPaths(t.Context(), enrolled(core.PipelineRepoModeOff), buildjob.PathsRequest{Repository: "acme/widgets", Base: m, Head: m}); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("paths for a repository that is off: %v", err)
	}
	if len(w.daemons.calls) != 0 {
		t.Fatalf("a refused job reached a daemon: %v", w.daemons.calls)
	}
}

func TestDispatchErrors(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	paths := buildjob.PathsRequest{Repository: "acme/widgets", Base: sha, Head: sha}
	repo := enrolled(core.PipelineRepoModeOn)
	for _, tc := range []struct {
		name    string
		pools   *fakePools
		daemons map[string]handler
		want    error
	}{
		{"no pool", &fakePools{}, nil, jobrun.ErrNoMember},
		{"no connected member", gatePool(), nil, jobrun.ErrNoMember},
		{"every member busy", gatePool(member("d-a", "a")), map[string]handler{
			"d-a": answer(buildjob.StartReply{State: buildjob.JobBusy}, buildjob.StatusReply{}),
		}, jobrun.ErrPoolBusy},
		{"every member old", gatePool(member("d-a", "a"), member("d-b", "b")), map[string]handler{"d-a": oldDaemon, "d-b": oldDaemon}, jobrun.ErrRefused},
		{"the job failed", gatePool(member("d-a", "a")), map[string]handler{
			"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobDone, Error: "git fetch: boom"}),
		}, jobrun.ErrJobFailed},
		{"the member forgot the job", gatePool(member("d-a", "a")), map[string]handler{
			"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobUnknown}),
		}, jobrun.ErrLost},
		{"a done job with no result", gatePool(member("d-a", "a")), map[string]handler{
			"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobDone}),
		}, jobrun.ErrUnverified},
		{"unsorted paths", gatePool(member("d-a", "a")), map[string]handler{
			"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobDone, Paths: &buildjob.PathsResult{Paths: []string{"b", "a"}}}),
		}, jobrun.ErrUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := jobrun.NewBuilder(jobrun.Deps{Pools: tc.pools, Daemons: &fakeDaemons{daemons: tc.daemons}, GitHub: clients{}, PollInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.ChangedPaths(t.Context(), repo, paths); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestACanceledJobIsCanceledOnItsMember(t *testing.T) {
	t.Parallel()
	d := &fakeDaemons{daemons: map[string]handler{
		"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobRunning}),
	}}
	b, _ := jobrun.NewBuilder(jobrun.Deps{Pools: gatePool(member("d-a", "a")), Daemons: d, GitHub: clients{}, PollInterval: time.Millisecond})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	sha := strings.Repeat("a", 40)
	if _, err := b.ChangedPaths(ctx, enrolled(core.PipelineRepoModeOn), buildjob.PathsRequest{Repository: "acme/widgets", Base: sha, Head: sha}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if got := d.sentTo("d-a"); !slices.Contains(got, buildjob.CommandCancel) {
		t.Fatalf("sent %v, want a cancel", got)
	}
}

// buildOn runs a real build on the world's host directly, for a report to
// tamper with.
func (w *world) buildOn(req buildjob.BuildRequest) buildjob.BuildResult {
	w.t.Helper()
	job := buildjob.StartRequest{JobID: "build-direct", Kind: buildjob.KindBuild, Build: &req}
	if st := w.host.Start(w.t.Context(), job); st.State != buildjob.JobRunning {
		w.t.Fatalf("start: %+v", st)
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st := w.host.Status(job.JobID); st.State == buildjob.JobDone {
			if st.Build == nil {
				w.t.Fatalf("build: %s", st.Error)
			}
			return *st.Build
		}
	}
	w.t.Fatal("the build did not finish")
	return buildjob.BuildResult{}
}

func TestVerifyBuildRefusesAReportGitHubDoesNotBearOut(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	one := w.pr("one", map[string]string{"x.txt": "x1\n"})
	two := w.pr("two", map[string]string{"z.txt": "z\n"})
	m := w.r.Head("main")
	req := buildjob.BuildRequest{Repository: "acme/widgets", BatchID: "b1", Base: m, Entries: []buildjob.Entry{one, two}}
	honest := w.buildOn(req)
	if err := jobrun.VerifyBuild(t.Context(), w.g, req, honest); err != nil {
		t.Fatalf("the honest report: %v", err)
	}
	other := w.r.Commit("unrelated", "unrelated", map[string]string{"q.txt": "q\n"})
	for name, lie := range map[string]func(r *buildjob.BuildResult){
		"another base":        func(r *buildjob.BuildResult) { r.Base = other },
		"another ref":         func(r *buildjob.BuildResult) { r.Ref = buildjob.QueueRef("b2") },
		"a commit not on ref": func(r *buildjob.BuildResult) { r.Commit = other },
		"a wrong last tree":   func(r *buildjob.BuildResult) { r.Trees[1] = w.r.Tree(other) },
		"a wrong first tree":  func(r *buildjob.BuildResult) { r.Trees[0] = r.Trees[1] },
		"a tree too few":      func(r *buildjob.BuildResult) { r.Trees = r.Trees[1:] },
		"an empty claim":      func(r *buildjob.BuildResult) { r.Included = nil },
		"a regen never pushed": func(r *buildjob.BuildResult) {
			r.Regenerated = []buildjob.Regenerated{{Entry: two, NewHead: other, Base: m}}
		},
		"a regen of other code": func(r *buildjob.BuildResult) {
			r.Regenerated = []buildjob.Regenerated{{Entry: two, NewHead: two.Head, Base: m}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := honest
			r.Trees = slices.Clone(honest.Trees)
			lie(&r)
			if err := jobrun.VerifyBuild(t.Context(), w.g, req, r); err == nil {
				t.Fatalf("a report with %s verified", name)
			}
		})
	}
}

func TestConfigReaderReadsOnTheEnrollingUsersPool(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sha := w.r.Commit("main", "add pipeline.yaml", map[string]string{".reliant/pipeline.yaml": "version: 1\n"})
	cr, err := jobrun.NewConfigReader(w.deps())
	if err != nil {
		t.Fatal(err)
	}
	read := func(user, path string) ([]byte, error) {
		return cr.Read(t.Context(), prrecords.ContentsRequest{UserID: user, Owner: "acme", Repo: "widgets", Path: path, Ref: sha})
	}
	if b, err := read("u1", ".reliant/pipeline.yaml"); err != nil || string(b) != "version: 1\n" {
		t.Fatalf("pipeline.yaml: %q, %v", b, err)
	}
	if _, err := read("u1", ".reliant/missing.yaml"); !errors.Is(err, prrecords.ErrContentsNotFound) {
		t.Fatalf("a missing file: %v", err)
	}
	if _, err := read("u2", ".reliant/pipeline.yaml"); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a user with no enrolment: %v", err)
	}
	if _, err := read("u1", "x.txt"); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a file outside .reliant/: %v", err)
	}
}
