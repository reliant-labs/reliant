// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// specFetcher records the FetchSpec it was given, then fetches like inner.
type specFetcher struct {
	inner Fetcher
	got   *FetchSpec
}

func (f specFetcher) Fetch(ctx context.Context, spec FetchSpec) error {
	*f.got = spec
	return f.inner.Fetch(ctx, spec)
}

func TestTheRunnerFetchesTheBaseAndNamesItInTheGateEnv(t *testing.T) {
	base := strings.Repeat("b", 40)
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, nil)
	var got FetchSpec
	r.Fetcher = specFetcher{inner: r.Fetcher, got: &got}
	req := startReq(cmdStage(config.StageLint, "true"), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, "true"), offStage(config.StageSBOMScan))
	req.BaseSHA = base
	// The batch's real base wins over a value gate.env sets.
	req.Env = map[string]string{"CI_BASE": "origin/main", "GOFLAGS": "-mod=mod"}
	if g, _ := runGate(t, f, r, req); g.Class != gaterun.ClassPass {
		t.Fatalf("result %+v", g)
	}
	if got.BaseSHA != base || got.SHA != testSHA {
		t.Fatalf("fetch spec %+v, want the base %s beside the candidate", got, base)
	}
	env := f.specs[0].Env
	for _, want := range []string{"GATE_BASE=" + base, "CI_BASE=" + base, "GOFLAGS=-mod=mod"} {
		if !slices.Contains(env, want) {
			t.Errorf("the gate env lacks %s: %v", want, env)
		}
	}
	if req.Env["CI_BASE"] != "origin/main" {
		t.Fatal("the request's own env was changed")
	}

	// No base (an older server, or main's own tip): neither is set.
	f = newFake(t, gaterun.IsolationSysbox)
	r = newRunner(t, f, nil)
	req.BaseSHA, req.Env = "", nil
	runGate(t, f, r, req)
	for _, kv := range f.specs[0].Env {
		if strings.HasPrefix(kv, "GATE_BASE=") || strings.HasPrefix(kv, "CI_BASE=") {
			t.Fatalf("a gate with no base sets %s", kv)
		}
	}
}

func TestStartRequestBaseIsAdditiveOnTheWire(t *testing.T) {
	req := startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"), offStage(config.StageBuild), offStage(config.StageSBOMScan))
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "base_sha") {
		t.Fatalf("a request with no base sends base_sha: %s", b)
	}
	req.BaseSHA = strings.Repeat("b", 40)
	b, _ = json.Marshal(req)
	// A daemon that predates the field decodes the payload and ignores it.
	var old struct {
		GateID string `json:"gate_id"`
		SHA    string `json:"sha"`
	}
	if err := json.Unmarshal(b, &old); err != nil || old.SHA != testSHA {
		t.Fatalf("an older daemon's decode: %+v %v", old, err)
	}
	var back gaterun.StartRequest
	if err := json.Unmarshal(b, &back); err != nil || back.BaseSHA != req.BaseSHA {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
	back.BaseSHA = "main"
	if err := back.Validate(); !errors.Is(err, gaterun.ErrInvalidRequest) {
		t.Fatalf("a base that is not a full sha: %v", err)
	}
}

// baseRemote is a directory of bare repositories holding acme/widgets with
// main at a base three commits deep and reliant-queue/b1 two squash commits
// over it, the shape the candidate builder pushes.
func baseRemote(t *testing.T) (remote, base, sha string) {
	t.Helper()
	remote = t.TempDir()
	bare := filepath.Join(remote, "acme", "widgets.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "-q", "--bare")
	work := t.TempDir()
	git(t, work, "init", "-q")
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", ".")
		git(t, work, "commit", "-q", "-m", name)
		return git(t, work, "rev-parse", "HEAD")
	}
	commit("one")
	commit("two")
	base = commit("three")
	git(t, work, "push", "-q", bare, "HEAD:refs/heads/main")
	commit("pr-a")
	sha = commit("pr-b")
	git(t, work, "push", "-q", bare, "HEAD:refs/heads/reliant-queue/b1")
	return remote, base, sha
}

func TestGitFetcherFetchesTheBaseForMergeBaseAndDiff(t *testing.T) {
	remote, base, sha := baseRemote(t)
	f := GitFetcher{BaseURL: "file://" + remote, Env: gitEnv}
	dir := t.TempDir()
	if err := f.Fetch(context.Background(), FetchSpec{Repo: "acme/widgets", Ref: "reliant-queue/b1", SHA: sha, BaseSHA: base, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("checked out %s, want %s", got, sha)
	}
	if got := git(t, dir, "rev-parse", BaseRef); got != base {
		t.Fatalf("%s is %s, want the base %s", BaseRef, got, base)
	}
	if got := git(t, dir, "merge-base", "HEAD", base); got != base {
		t.Fatalf("merge-base HEAD base = %s, want %s", got, base)
	}
	if got := git(t, dir, "diff", "--name-only", base+"...HEAD"); got != "pr-a\npr-b" {
		t.Fatalf("diff against the base names %q", got)
	}
	// Only what the candidate adds over the base is fetched: the base's own
	// history stays out (the checkout is shallow at the base).
	if got := git(t, dir, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("the checkout holds %s commits, want the base and the two over it", got)
	}
	if err := checkNoCredential(dir); err != nil {
		t.Fatal(err)
	}

	// A base that is no commit of the repository fails the fetch.
	err := f.Fetch(context.Background(), FetchSpec{Repo: "acme/widgets", Ref: "reliant-queue/b1", SHA: sha, BaseSHA: testSHA, Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "fetch the base") {
		t.Fatalf("an unknown base: %v", err)
	}
}
