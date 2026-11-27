// Copyright (c) 2025 Reliant Labs
package candidate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// host is a job host over the fixture's fake GitHub: its remote is the fake
// repository's directory, so its fetches and pushes are real git.
func (f *fixture) host() *candidate.Host {
	return &candidate.Host{
		Root:      filepath.Join(f.t.TempDir(), "queue-jobs"),
		GitHub:    f.g,
		RemoteURL: func(string) string { return f.r.URL() },
	}
}

// runJob starts req through Handle, as the daemon's command handler does,
// and polls it to done.
func runJob(t *testing.T, h *candidate.Host, req buildjob.StartRequest) buildjob.StatusReply {
	t.Helper()
	ctx := t.Context()
	payload, _ := json.Marshal(req)
	raw, err := h.Handle(ctx, buildjob.CommandStart, payload)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var start buildjob.StartReply
	if err := json.Unmarshal(raw, &start); err != nil {
		t.Fatal(err)
	}
	if start.State != buildjob.JobRunning && start.State != buildjob.JobDone {
		t.Fatalf("start: %+v", start)
	}
	status, _ := json.Marshal(buildjob.StatusRequest{JobID: req.JobID})
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		raw, err := h.Handle(ctx, buildjob.CommandStatus, status)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var reply buildjob.StatusReply
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.State == buildjob.JobDone {
			return reply
		}
		if reply.State != buildjob.JobRunning {
			t.Fatalf("status: %+v", reply)
		}
	}
	t.Fatalf("job %s did not finish", req.JobID)
	return buildjob.StatusReply{}
}

func wireEntries(es ...candidate.Entry) []buildjob.Entry {
	out := make([]buildjob.Entry, len(es))
	for i, e := range es {
		out[i] = buildjob.Entry{Number: e.Number, Head: e.Head, Title: e.Title}
	}
	return out
}

func TestHostBuildsTheCandidateAndPushesTheQueueRef(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one := f.pr("one", map[string]string{"x.txt": "x1\n"})
	two := f.pr("two", map[string]string{"two.txt": "2\n"})
	m := f.r.Head("main")
	h := f.host()
	reply := runJob(t, h, buildjob.StartRequest{JobID: "build-1", Kind: buildjob.KindBuild, Build: &buildjob.BuildRequest{
		Repository: "acme/widgets", BatchID: "b1", Base: m, Entries: wireEntries(one, two),
	}})
	if reply.Error != "" || reply.Build == nil {
		t.Fatalf("reply: %+v", reply)
	}
	res := *reply.Build
	if res.Base != m || res.Ref != buildjob.QueueRef("b1") || len(res.Trees) != 2 || len(res.Included) != 2 {
		t.Fatalf("result: %+v", res)
	}
	if got := f.r.Head("reliant-queue/b1"); got != res.Commit {
		t.Fatalf("queue ref at %s, the report says %s", got, res.Commit)
	}
	if f.r.Tree(res.Commit) != res.Trees[1] {
		t.Fatal("the candidate's tree is not T_2")
	}

	// The same job id answers for the job it already ran.
	payload, _ := json.Marshal(buildjob.StartRequest{JobID: "build-1", Kind: buildjob.KindBuild, Build: &buildjob.BuildRequest{
		Repository: "acme/widgets", BatchID: "b1", Base: m, Entries: wireEntries(one, two),
	}})
	raw, err := h.Handle(t.Context(), buildjob.CommandStart, payload)
	if err != nil || !strings.Contains(string(raw), `"done"`) {
		t.Fatalf("restart: %s, %v", raw, err)
	}
	// The git cache holds no credential and no remote.
	cfg, err := os.ReadFile(filepath.Join(h.Root, "acme", "widgets.git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "credential") || strings.Contains(string(cfg), "url") {
		t.Fatalf("the git cache's config holds a remote or a credential:\n%s", cfg)
	}
}

func TestHostShadowBuildPushesOnlyTheQueueRef(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one, two := generatedConflict(f)
	m := f.r.Head("main")
	twoHead := f.r.Head("two")
	reply := runJob(t, f.host(), buildjob.StartRequest{JobID: "build-shadow", Kind: buildjob.KindBuild, Build: &buildjob.BuildRequest{
		Repository: "acme/widgets", BatchID: "b2", Base: m, Entries: wireEntries(one, two),
		Generated: genGlobs, Regen: "sh regen.sh", Shadow: true,
	}})
	if reply.Error != "" || reply.Build == nil {
		t.Fatalf("reply: %+v", reply)
	}
	if f.r.Head("two") != twoHead {
		t.Fatal("a shadow build moved a PR's branch")
	}
	if len(reply.Build.WouldPush) != 1 || reply.Build.WouldPush[0].Ref != "refs/heads/two" {
		t.Fatalf("would push %+v", reply.Build.WouldPush)
	}
	if f.r.Head("reliant-queue/b2") != reply.Build.Commit {
		t.Fatal("the shadow build did not push its queue ref")
	}
}

func TestHostChangedPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e := f.pr("one", map[string]string{"x.txt": "x1\n", "new/file.txt": "n\n"})
	reply := runJob(t, f.host(), buildjob.StartRequest{JobID: "paths-1", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{
		Repository: "acme/widgets", Base: f.r.Head("main"), Head: e.Head,
	}})
	if reply.Error != "" || reply.Paths == nil || !slices.Equal(reply.Paths.Paths, []string{"new/file.txt", "x.txt"}) {
		t.Fatalf("reply: %+v", reply)
	}
}

func TestHostReadsAConfigFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.r.Commit("main", "add pipeline.yaml", map[string]string{
		".reliant/pipeline.yaml": "version: 1\n",
		".reliant/big.yaml":      strings.Repeat("x", buildjob.ConfigMaxBytes+1),
	})
	h := f.host()
	read := func(id, path string) buildjob.StatusReply {
		return runJob(t, h, buildjob.StartRequest{JobID: id, Kind: buildjob.KindConfig, Config: &buildjob.ConfigRequest{
			Repository: "acme/widgets", SHA: sha, Path: path,
		}})
	}
	if r := read("config-1", ".reliant/pipeline.yaml"); r.Error != "" || r.Config == nil || !r.Config.Found || string(r.Config.Bytes) != "version: 1\n" {
		t.Fatalf("pipeline.yaml: %+v", r)
	}
	if r := read("config-2", ".reliant/missing.yaml"); r.Error != "" || r.Config == nil || r.Config.Found {
		t.Fatalf("a missing file: %+v", r)
	}
	if r := read("config-3", ".reliant/big.yaml"); r.Error == "" || !strings.Contains(r.Error, "at most") {
		t.Fatalf("an oversized file: %+v", r)
	}
	// Only files under .reliant/ are read.
	payload, _ := json.Marshal(buildjob.StartRequest{JobID: "config-4", Kind: buildjob.KindConfig, Config: &buildjob.ConfigRequest{
		Repository: "acme/widgets", SHA: sha, Path: "x.txt",
	}})
	raw, _ := h.Handle(t.Context(), buildjob.CommandStart, payload)
	if !strings.Contains(string(raw), `"refused"`) {
		t.Fatalf("a read outside .reliant/: %s", raw)
	}
}

func TestHostPrunesAJobCacheAndTheNextJobRefetches(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e := f.pr("one", map[string]string{"x.txt": "x1\n"})
	h := f.host()
	paths := func(id string) buildjob.StatusReply {
		return runJob(t, h, buildjob.StartRequest{JobID: id, Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{
			Repository: "acme/widgets", Base: f.r.Head("main"), Head: e.Head,
		}})
	}
	if r := paths("paths-1"); r.Error != "" {
		t.Fatalf("first job: %+v", r)
	}
	cache := filepath.Join(h.Root, "acme", "widgets.git")
	lease, err := os.Stat(cache + ".lease")
	if err != nil || time.Since(lease.ModTime()) > time.Minute {
		t.Fatalf("the job did not stamp its lease: %v", err)
	}

	// Just used: kept by age and by the size eviction's idle floor, and gc'd.
	res := h.Prune(t.Context(), candidate.PrunePolicy{MaxBytes: 1, GC: true})
	if len(res.Caches) != 1 || res.Caches[0].Repository != "acme/widgets" || res.Caches[0].Outcome != candidate.PruneKept || res.Caches[0].Err != "" {
		t.Fatalf("a fresh cache: %+v", res.Caches)
	}
	// Unused for longer than the age limit: removed.
	res = h.Prune(t.Context(), candidate.PrunePolicy{MaxAge: time.Nanosecond})
	if len(res.Caches) != 1 || res.Caches[0].Outcome != candidate.PruneRemovedAge || res.Freed == 0 {
		t.Fatalf("an aged cache: %+v", res)
	}
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cache is still there: %v", err)
	}
	if r := paths("paths-2"); r.Error != "" || r.Paths == nil || !slices.Equal(r.Paths.Paths, []string{"x.txt"}) {
		t.Fatalf("the job after the prune: %+v", r)
	}
}

func TestHostRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	h := &candidate.Host{Root: t.TempDir()}
	sha := strings.Repeat("a", 40)
	for name, req := range map[string]buildjob.StartRequest{
		"no body":      {JobID: "j1", Kind: buildjob.KindBuild},
		"two bodies":   {JobID: "j2", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{Repository: "a/b", Base: sha, Head: sha}, Config: &buildjob.ConfigRequest{Repository: "a/b", SHA: sha, Path: ".reliant/x"}},
		"wrong kind":   {JobID: "j3", Kind: buildjob.KindBuild, Paths: &buildjob.PathsRequest{Repository: "a/b", Base: sha, Head: sha}},
		"bad job id":   {JobID: "J/4", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{Repository: "a/b", Base: sha, Head: sha}},
		"bad sha":      {JobID: "j5", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{Repository: "a/b", Base: "main", Head: sha}},
		"escaping cfg": {JobID: "j6", Kind: buildjob.KindConfig, Config: &buildjob.ConfigRequest{Repository: "a/b", SHA: sha, Path: ".reliant/../x"}},
	} {
		if got := h.Start(context.Background(), req); got.State != buildjob.JobRefused {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := h.Status("nope"); got.State != buildjob.JobUnknown {
		t.Fatalf("unknown job: %+v", got)
	}
}

func TestGitCredentialEnvAnswersGitWithTheTokenAndWritesNothing(t *testing.T) {
	t.Parallel()
	const token = "ghs_TESTSECRET0123456789abcdefghijklmn" //nolint:gosec // G101: a test value.
	env := gitport.HermeticEnv(gitport.Identity{}, candidate.GitCredentialEnv(token)...)
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") && strings.Contains(kv, token) {
			t.Fatalf("the token is in git's config, not only its environment: %s", kv)
		}
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "git", "credential", "fill")
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=acme/widgets.git\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git credential fill: %v", err)
	}
	if !strings.Contains(string(out), "username=x-access-token\n") || !strings.Contains(string(out), "password="+token+"\n") {
		t.Fatalf("git credential fill answered:\n%s", out)
	}
	// Another host gets nothing from it.
	cmd = exec.CommandContext(t.Context(), "git", "credential", "fill")
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader("protocol=https\nhost=example.com\n\n")
	if out, _ := cmd.Output(); strings.Contains(string(out), token) {
		t.Fatalf("the helper answered another host:\n%s", out)
	}
	if candidate.GitCredentialEnv("") != nil {
		t.Fatal("no token must add no environment")
	}
}

// pushPort records the pushes a job's git makes.
type pushPort struct {
	gitport.Port
	pushed []gitport.PushInput
}

func (p *pushPort) Push(_ context.Context, _ string, in gitport.PushInput) error {
	p.pushed = append(p.pushed, in)
	return nil
}

func TestHostPushesOnlyWhereABuildMay(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("b", 40)
	build := buildjob.StartRequest{JobID: "j", Kind: buildjob.KindBuild, Build: &buildjob.BuildRequest{
		Repository: "acme/widgets", BatchID: "b1", Base: strings.Repeat("a", 40), Entries: []buildjob.Entry{{Number: 1, Head: head}},
	}}
	paths := buildjob.StartRequest{JobID: "p", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{Repository: "acme/widgets"}}
	zero := strings.Repeat("0", 40)
	for _, tc := range []struct {
		name string
		req  buildjob.StartRequest
		in   gitport.PushInput
		ok   bool
	}{
		{"the queue ref", build, gitport.PushInput{Ref: "refs/heads/reliant-queue/b1", ExpectedOld: zero}, true},
		{"a PR branch from a queued head", build, gitport.PushInput{Ref: "refs/heads/two", ExpectedOld: head}, true},
		{"another batch's queue ref", build, gitport.PushInput{Ref: "refs/heads/reliant-queue/b2", ExpectedOld: zero}, false},
		{"a branch from a head not queued", build, gitport.PushInput{Ref: "refs/heads/main", ExpectedOld: strings.Repeat("c", 40)}, false},
		{"a push without a lease", build, gitport.PushInput{Ref: "refs/heads/reliant-queue/b1"}, false},
		{"a tag", build, gitport.PushInput{Ref: "refs/tags/v1", ExpectedOld: head}, false},
		{"any push of a paths job", paths, gitport.PushInput{Ref: "refs/heads/reliant-queue/b1", ExpectedOld: zero}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pushPort{}
			err := candidate.GuardPushes(p, tc.req).Push(context.Background(), "url", tc.in)
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, candidate.ErrPushRefused)) {
				t.Fatalf("push %+v: %v", tc.in, err)
			}
			if tc.ok != (len(p.pushed) == 1) {
				t.Fatalf("pushed %+v", p.pushed)
			}
		})
	}
}
