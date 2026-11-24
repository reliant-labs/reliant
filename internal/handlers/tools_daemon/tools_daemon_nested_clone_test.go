// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeNestedStore is project_daemon_repos and one checkout row in memory.
type fakeNestedStore struct {
	rows        []*core.ProjectDaemonRepo
	installed   bool
	failedWith  string
	claimCalled bool
}

func (f *fakeNestedStore) ClaimProjectDaemonRepos(_ context.Context, _, _ string, _ time.Time) ([]*core.ProjectDaemonRepo, error) {
	f.claimCalled = true
	var out []*core.ProjectDaemonRepo
	for _, r := range f.rows {
		if r.State == core.NestedRepoPending {
			r.State = core.NestedRepoCloning
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeNestedStore) SettleProjectDaemonRepo(_ context.Context, _, _, repoID, installErr string) error {
	for _, r := range f.rows {
		if r.RepoID == repoID && r.State == core.NestedRepoCloning {
			r.State, r.Error = core.NestedRepoInstalled, ""
			if installErr != "" {
				r.State, r.Error = core.NestedRepoFailed, installErr
			}
		}
	}
	return nil
}

func (f *fakeNestedStore) ListProjectDaemonReposForPair(context.Context, string, string) ([]*core.ProjectDaemonRepo, error) {
	return f.rows, nil
}

func (f *fakeNestedStore) MarkProjectDaemonInstalledInScope(context.Context, string, string, *core.OrgScope) (bool, error) {
	f.installed = true
	return true, nil
}

func (f *fakeNestedStore) FailProjectDaemonInstallInScope(_ context.Context, _, _, installErr string, _ *core.OrgScope) (bool, error) {
	f.failedWith = installErr
	return true, nil
}

func nestedRow(id, name, rel, remote string) *core.ProjectDaemonRepo {
	return &core.ProjectDaemonRepo{ProjectID: "p1", DaemonID: "d1", RepoID: id, Name: name, RelativePath: rel, RemoteURL: remote, State: core.NestedRepoPending}
}

func landed(context.Context) (bool, error) { return true, nil }

func TestCloneNestedRepos_ClonesEachIntoItsPathShallowestFirst(t *testing.T) {
	store := &fakeNestedStore{rows: []*core.ProjectDaemonRepo{
		nestedRow("r-deep", "inner", "tools/inner", "git@github.com:acme/inner.git"),
		nestedRow("r-tools", "tools", "tools", "https://github.com/acme/tools"),
		nestedRow("r-forge", "forge", "forge", "git@github.com:acme/forge.git"),
	}}
	var ran []cloneStep
	run := func(_ context.Context, step cloneStep) error { ran = append(ran, step); return nil }

	if !cloneNestedRepos(context.Background(), store, run, landed, "p1", "d1", "/srv/projects/acme", nil) {
		t.Fatal("the checkout was not settled")
	}
	want := []cloneStep{
		{Repo: "git@github.com:acme/forge.git", Path: "/srv/projects/acme/forge"},
		{Repo: "https://github.com/acme/tools", Path: "/srv/projects/acme/tools"},
		{Repo: "git@github.com:acme/inner.git", Path: "/srv/projects/acme/tools/inner"},
	}
	if len(ran) != len(want) {
		t.Fatalf("clones = %+v, want %+v", ran, want)
	}
	for i := range want {
		if ran[i] != want[i] {
			t.Errorf("clone %d = %+v, want %+v", i, ran[i], want[i])
		}
	}
	if !store.installed || store.failedWith != "" {
		t.Fatalf("checkout = installed %v, failed %q; want installed", store.installed, store.failedWith)
	}
}

func TestCloneNestedRepos_AFailureNamesTheRepositoryAndGitsError(t *testing.T) {
	store := &fakeNestedStore{rows: []*core.ProjectDaemonRepo{
		nestedRow("r-forge", "forge", "forge", "git@github.com:acme/forge.git"),
		nestedRow("r-in", "plugin", "forge/plugin", "https://github.com/acme/plugin"),
		nestedRow("r-web", "web", "web", "https://github.com/acme/web"),
	}}
	gitErr := "git clone failed: exit status 128: git@github.com: Permission denied (publickey)."
	var ran []string
	run := func(_ context.Context, step cloneStep) error {
		ran = append(ran, step.Path)
		if strings.HasSuffix(step.Path, "/forge") {
			return errors.New(gitErr)
		}
		return nil
	}

	cloneNestedRepos(context.Background(), store, run, landed, "p1", "d1", "/w/acme", nil)

	// Nothing is cloned inside a repository that failed: its clone would
	// create forge/ and forge could never be cloned there again.
	if strings.Join(ran, ",") != "/w/acme/forge,/w/acme/web" {
		t.Fatalf("clones = %v, want forge then web, and nothing inside forge", ran)
	}
	if store.installed {
		t.Fatal("a checkout missing a nested repository must not read as installed")
	}
	for _, want := range []string{"forge (forge/)", gitErr, "plugin (forge/plugin/)"} {
		if !strings.Contains(store.failedWith, want) {
			t.Errorf("install error %q does not name %q", store.failedWith, want)
		}
	}
	if strings.Contains(store.failedWith, "web (web/)") {
		t.Errorf("install error %q names web, which cloned", store.failedWith)
	}
	for _, r := range store.rows {
		if r.RepoID == "r-forge" && (r.State != core.NestedRepoFailed || r.Error != gitErr) {
			t.Errorf("forge's own row = %s %q, want failed with git's error", r.State, r.Error)
		}
	}
}

func TestCloneNestedRepos_WaitsForTheRoot(t *testing.T) {
	store := &fakeNestedStore{rows: []*core.ProjectDaemonRepo{nestedRow("r1", "forge", "forge", "https://github.com/acme/forge")}}
	notYet := func(context.Context) (bool, error) { return false, nil }
	run := func(context.Context, cloneStep) error { t.Fatal("cloned before the root landed"); return nil }

	if cloneNestedRepos(context.Background(), store, run, notYet, "p1", "d1", "/w/acme", nil) {
		t.Fatal("settled a checkout whose root has not landed")
	}
	if store.claimCalled || store.installed || store.failedWith != "" {
		t.Fatalf("store touched before the root landed: %+v", store)
	}
}

func TestCloneNestedRepos_LeavesTheSettleToWhoeverHoldsAClaim(t *testing.T) {
	// Another gateway is cloning one; this one has nothing to claim.
	busy := nestedRow("r1", "forge", "forge", "https://github.com/acme/forge")
	busy.State = core.NestedRepoCloning
	store := &fakeNestedStore{rows: []*core.ProjectDaemonRepo{busy}}
	run := func(context.Context, cloneStep) error {
		t.Fatal("cloned a repository another gateway holds")
		return nil
	}

	if cloneNestedRepos(context.Background(), store, run, landed, "p1", "d1", "/w/acme", nil) {
		t.Fatal("settled the checkout while a nested clone is still running")
	}
	if store.installed || store.failedWith != "" {
		t.Fatalf("checkout settled: %+v", store)
	}
}

func TestNestedFailureReason(t *testing.T) {
	if got := nestedFailureReason([]*core.ProjectDaemonRepo{{Name: "a", RelativePath: "a", State: core.NestedRepoInstalled}}); got != "" {
		t.Fatalf("reason with no failure = %q", got)
	}
	long := strings.Repeat("x", 3000)
	got := nestedFailureReason([]*core.ProjectDaemonRepo{{Name: "a", RelativePath: "a", State: core.NestedRepoFailed, Error: long}})
	if len(got) > nestedFailureReasonMax || !strings.HasPrefix(got, "the project cloned, but its nested repository a (a/): ") {
		t.Fatalf("reason = %q (len %d)", got[:80], len(got))
	}
}
