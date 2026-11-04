// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type restoreStore struct {
	labels    map[string]string
	pools     []*db.MachinePool
	rows      []*core.ProjectDaemon
	repos     map[string][]*core.Repo
	installed []string // projects recorded installed (verified) again
	queued    map[string]string
	failed    map[string]string // projectID -> install error
}

func (s *restoreStore) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	return &db.Daemon{ID: id, Labels: s.labels}, nil
}

func (s *restoreStore) ListMachinePoolsForUser(context.Context, string) ([]*db.MachinePool, error) {
	return s.pools, nil
}

func (s *restoreStore) ListProjectDaemonsForDaemon(context.Context, string) ([]*core.ProjectDaemon, error) {
	return s.rows, nil
}

func (s *restoreStore) ListReposByProject(_ context.Context, projectID string) ([]*core.Repo, error) {
	return s.repos[projectID], nil
}

func (s *restoreStore) UpsertProjectDaemon(_ context.Context, projectID, _, _ string, _ *string) error {
	s.installed = append(s.installed, projectID)
	return nil
}

func (s *restoreStore) UpsertQueuedProjectDaemon(_ context.Context, projectID, _, _ string, _ *string, requestID string) error {
	if s.queued == nil {
		s.queued = map[string]string{}
	}
	s.queued[requestID] = projectID
	return nil
}

func (s *restoreStore) ResolveQueuedProjectDaemon(_ context.Context, requestID, installErr string) error {
	if s.failed == nil {
		s.failed = map[string]string{}
	}
	s.failed[s.queued[requestID]] = installErr
	return nil
}

func strp(s string) *string { return &s }

// newRestoreWorld is a cicd pool member whose disk came up empty: project
// p-multi (three nested repositories, no root remote) and p-single (a root
// remote only) are installed, p-other is not installed here.
func newRestoreWorld() (*restoreStore, []*db.Project) {
	s := &restoreStore{
		labels: map[string]string{"pool": "cicd"},
		pools:  []*db.MachinePool{{Name: "cicd", Selector: map[string]string{"pool": "cicd"}}},
		rows: []*core.ProjectDaemon{
			{ProjectID: "p-multi", Path: "/home/workspace/projects/reliant-labs", InstallState: core.ProjectInstallInstalled},
			{ProjectID: "p-single", Path: "/home/workspace/projects/app", InstallState: core.ProjectInstallInstalled, DefaultBranch: strp("trunk")},
			{ProjectID: "p-other", Path: "/home/workspace/projects/other", InstallState: core.ProjectInstallFailed},
		},
		repos: map[string][]*core.Repo{
			"p-multi": {
				{Name: "reliant", RelativePath: "reliant", RemoteURL: strp("https://github.com/reliant-labs/reliant.git")},
				{Name: "control-plane", RelativePath: "control-plane", RemoteURL: strp("https://github.com/reliant-labs/control-plane.git")},
				{Name: "scratch", RelativePath: "scratch"}, // no remote: nothing to clone
			},
		},
	}
	missing := []*db.Project{
		{ID: "p-multi", Path: "/home/workspace/projects/reliant-labs"},
		{ID: "p-single", Path: "/home/workspace/projects/app", RemoteURL: strp("https://github.com/acme/app.git")},
		{ID: "p-other", Path: "/home/workspace/projects/other", RemoteURL: strp("https://github.com/acme/other.git")},
	}
	return s, missing
}

// A pool member whose checkouts are gone (its disk is the pod's own) gets
// each installed project re-cloned, every repository at its path, and the
// row recorded installed (verified) again, so placement can use it.
func TestRestorePoolMemberCheckouts_ReclonesInstalledProjects(t *testing.T) {
	s, missing := newRestoreWorld()
	var ran []cloneStep
	run := func(_ context.Context, step cloneStep) error { ran = append(ran, step); return nil }

	restorePoolMemberCheckouts(context.Background(), s, run, "d-1", "user-1", missing)

	want := []cloneStep{
		{Repo: "https://github.com/reliant-labs/control-plane.git", Path: "/home/workspace/projects/reliant-labs/control-plane"},
		{Repo: "https://github.com/reliant-labs/reliant.git", Path: "/home/workspace/projects/reliant-labs/reliant"},
		{Repo: "https://github.com/acme/app.git", Branch: "trunk", Path: "/home/workspace/projects/app"},
	}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("clones = %+v\nwant     %+v", ran, want)
	}
	if !reflect.DeepEqual(s.installed, []string{"p-multi", "p-single"}) {
		t.Fatalf("recorded installed = %v, want p-multi and p-single (not p-other, which was never installed here)", s.installed)
	}
	if len(s.failed) != 0 {
		t.Fatalf("failed = %v, want none", s.failed)
	}
}

// A machine in no pool is the owner's: a missing checkout there is their
// doing, and nothing is re-cloned behind their back.
func TestRestorePoolMemberCheckouts_LeavesNonMembersAlone(t *testing.T) {
	s, missing := newRestoreWorld()
	s.labels = map[string]string{"pool": "gpu"}
	ran := 0
	restorePoolMemberCheckouts(context.Background(), s, func(context.Context, cloneStep) error { ran++; return nil }, "d-1", "user-1", missing)
	if ran != 0 || len(s.installed) != 0 || len(s.failed) != 0 {
		t.Fatalf("ran %d clones, installed %v, failed %v on a machine in no pool; want nothing", ran, s.installed, s.failed)
	}
}

// A clone that fails marks the row failed with why, so the member's install
// state (and placement's reason) say so instead of waiting forever; the other
// projects are still restored.
func TestRestorePoolMemberCheckouts_FailureIsRecorded(t *testing.T) {
	s, missing := newRestoreWorld()
	run := func(_ context.Context, step cloneStep) error {
		if strings.Contains(step.Repo, "control-plane") {
			return errors.New("authentication failed")
		}
		return nil
	}
	restorePoolMemberCheckouts(context.Background(), s, run, "d-1", "user-1", missing)
	if got := s.failed["p-multi"]; !strings.Contains(got, "authentication failed") {
		t.Fatalf("p-multi failure = %q, want the clone's error", got)
	}
	if !reflect.DeepEqual(s.installed, []string{"p-single"}) {
		t.Fatalf("installed = %v, want p-single restored despite p-multi failing", s.installed)
	}
}

// A project whose checkout is gone and that has no remote anywhere cannot be
// restored; that is recorded, not silently waited on.
func TestRestorePoolMemberCheckouts_NoRemoteIsAFailure(t *testing.T) {
	s, _ := newRestoreWorld()
	s.repos = map[string][]*core.Repo{}
	restorePoolMemberCheckouts(context.Background(), s, func(context.Context, cloneStep) error { return nil }, "d-1", "user-1",
		[]*db.Project{{ID: "p-multi", Path: "/home/workspace/projects/reliant-labs"}})
	if got := s.failed["p-multi"]; !strings.Contains(got, "no remote") {
		t.Fatalf("failure = %q, want it to say there is no remote", got)
	}
}

// The root repository is cloned before anything nested inside it (a clone
// needs an empty target), with the branch the machine's row records.
func TestRestorePlan_RootFirst(t *testing.T) {
	row := &core.ProjectDaemon{Path: "/p", DefaultBranch: strp("main")}
	got := restorePlan(&db.Project{ID: "p"}, row, []*core.Repo{
		{RelativePath: "libs/inner", RemoteURL: strp("u-inner")},
		{RelativePath: "", RemoteURL: strp("u-root")},
		{RelativePath: "libs", RemoteURL: strp("u-libs")},
	})
	want := []cloneStep{
		{Repo: "u-root", Branch: "main", Path: "/p"},
		{Repo: "u-libs", Path: "/p/libs"},
		{Repo: "u-inner", Path: "/p/libs/inner"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %+v\nwant %+v", got, want)
	}
}
