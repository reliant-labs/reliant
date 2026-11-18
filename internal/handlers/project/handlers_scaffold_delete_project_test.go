package project_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// scheduling records each ScheduleProjectDeletion on store, answering as
// db.Repo does for a project that was not scheduled.
func scheduling(store *project.MockRepository) *[]string {
	var scheduled []string
	store.ScheduleProjectDeletionFunc = func(_ context.Context, id, by string, after time.Time) (core.ProjectDeletion, error) {
		scheduled = append(scheduled, id+" by "+by)
		return core.ProjectDeletion{DeleteAfter: after}, nil
	}
	store.DeleteProjectFunc = func(context.Context, string, string) error {
		return errors.New("DeleteProject no longer deletes: it schedules")
	}
	return &scheduled
}

func TestDeleteProject_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	scheduled := scheduling(store)
	now := time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, Now: func() time.Time { return now }}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteProjectRequest, pb.DeleteProjectResponse]{
		{
			Name:    "a project the caller does not own is not found, and not deleted",
			Req:     connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			// An old client calls DeleteProject and gets the scheduled
			// delete, still answered as a success.
			Name: "the caller's project is scheduled for deletion, restorable for the window",
			Req:  connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeleteProjectResponse]) {
				if !resp.Msg.GetSuccess() {
					t.Fatal("success = false")
				}
				if len(*scheduled) != 1 || (*scheduled)[0] != "p-1 by test-user" {
					t.Fatalf("scheduled = %v, want only p-1 by test-user", *scheduled)
				}
				if want := now.Add(core.ProjectDeleteWindow).Format(time.RFC3339); resp.Msg.GetDeleteAfter() != want {
					t.Fatalf("delete_after = %q, want one window from now, %q", resp.Msg.GetDeleteAfter(), want)
				}
				if len(resp.Msg.GetMachineRemovals()) != 0 {
					t.Fatalf("machine removals %v without remove_from_machines", resp.Msg.GetMachineRemovals())
				}
			},
		},
	}, svc.DeleteProject)
}

// perMachine is a router whose machines each answer for themselves.
type perMachine struct {
	fakeDaemon
	answer func(daemonID, commandType string, payload []byte) ([]byte, error)
}

func (p perMachine) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	return p.answer(daemonID, commandType, payload)
}

func machinesAnswering(answer func(daemonID, commandType string, payload []byte) ([]byte, error)) perMachine {
	return perMachine{
		fakeDaemon: func(string, []byte) ([]byte, error) { return []byte(`{}`), nil },
		answer:     answer,
	}
}

// remove_from_machines fans RemoveProjectFromDaemon's work out to every
// machine holding a checkout, and reports each: removed, offline (keeps its
// copy), or refused. The delete is scheduled whatever they answer.
func TestDeleteProjectRemovesTheCodeFromMachinesOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	scheduled := scheduling(store)
	store.ListProjectDaemonsForProjectFunc = func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
		return []*core.ProjectDaemon{
			{ProjectID: projectID, DaemonID: "laptop", Path: "/work/a", InstallState: core.ProjectInstallInstalled},
			{ProjectID: projectID, DaemonID: "offline", Path: "/work/b", InstallState: core.ProjectInstallInstalled},
		}, nil
	}
	// The fan-out asks machines concurrently.
	var mu sync.Mutex
	var removedRows []string
	store.DeleteProjectDaemonFunc = func(_ context.Context, _, daemonID string) error {
		mu.Lock()
		defer mu.Unlock()
		removedRows = append(removedRows, daemonID)
		return nil
	}
	var removes []string
	router := machinesAnswering(func(daemonID, commandType string, payload []byte) ([]byte, error) {
		if daemonID == "offline" {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected for user"))
		}
		switch commandType {
		case "git.remove":
			var p map[string]string
			_ = json.Unmarshal(payload, &p)
			mu.Lock()
			removes = append(removes, daemonID+":"+p["path"])
			mu.Unlock()
			return []byte(`{"success":true}`), nil
		}
		return []byte(`{}`), nil
	})
	svc := newService(t, store, router)

	resp, err := svc.DeleteProject(project.AuthedContext(t), connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-1", RemoveFromMachines: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(*scheduled) != 1 {
		t.Fatalf("scheduled = %v, want the delete scheduled whatever the machines answer", *scheduled)
	}
	got := map[string]pb.ProjectMachineRemovalStatus{}
	for _, r := range resp.Msg.GetMachineRemovals() {
		got[r.GetDaemonId()] = r.GetStatus()
	}
	want := map[string]pb.ProjectMachineRemovalStatus{
		"laptop":  pb.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_REMOVED,
		"offline": pb.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_OFFLINE,
	}
	if len(got) != 2 || got["laptop"] != want["laptop"] || got["offline"] != want["offline"] {
		t.Fatalf("machine removals = %v, want %v", got, want)
	}
	if len(removes) != 1 || removes[0] != "laptop:/work/a" {
		t.Fatalf("git.remove sent = %v, want only laptop's checkout at its own path", removes)
	}
	sort.Strings(removedRows)
	if len(removedRows) != 1 || removedRows[0] != "laptop" {
		t.Fatalf("checkout rows dropped = %v, want only the removed machine's (the offline one keeps its record)", removedRows)
	}

	// Without the opt-in, nothing on any machine is touched (D20).
	removes = nil
	if _, err := svc.DeleteProject(project.AuthedContext(t), connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-1"})); err != nil {
		t.Fatal(err)
	}
	if len(removes) != 0 {
		t.Fatalf("git.remove sent without remove_from_machines: %v", removes)
	}
}
