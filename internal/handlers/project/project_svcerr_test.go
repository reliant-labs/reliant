// Copyright (c) 2025 Reliant Labs
package project_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// opaqueSecret is a store failure nobody on this side of the wire wrote: its
// text must reach the log, never the client.
var opaqueSecret = errors.New("dsn=postgres://u:secret@h")

// storeFailures are the store errors every converted rpc class is pinned
// against: a verdict the store already made keeps its code, a deadline or a
// cancellation is the client's to see as such, and anything unclassified is
// Internal with the rpc's own sentence.
func storeFailures(notFound error) []struct {
	name string
	err  error
	code connect.Code
} {
	return []struct {
		name string
		err  error
		code connect.Code
	}{
		{"the store's not-found", notFound, connect.CodeNotFound},
		{"a deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"a cancellation", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"an opaque failure", opaqueSecret, connect.CodeInternal},
	}
}

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.Code(err), "err = %v", err)
	assert.NotContains(t, svcerr.ToConnect(err).Message(), "secret", "the store's text must not reach the client")
}

// ListProjects answered Internal for every store error, a deadline included,
// so a slow database read as a server fault (and paged as one).
func TestListProjects_StoreErrorClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range storeFailures(svcerr.NotFound("project")) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newService(t, &project.StubRepo{
				ListProjectsFunc: func(context.Context, db.ProjectFilters) ([]*db.Project, error) { return nil, tc.err },
			}, nil)
			_, err := svc.ListProjects(project.AuthedContext(t), connect.NewRequest(&pb.ListProjectsRequest{}))
			requireCode(t, err, tc.code)
			if tc.code == connect.CodeInternal {
				assert.Equal(t, "failed to list projects", svcerr.ToConnect(err).Message())
			}
		})
	}
}

// The ownership check: someone else's project is the store's one NotFound,
// so the answer never reveals that it exists; a failure to look it up is the
// failure it is, no longer "not found".
func TestProjectOwnership_StoreErrorClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range storeFailures(core.ErrProjectNotFound) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newService(t, &project.StubRepo{
				GetProjectWithUserCheckFunc: func(context.Context, string, string) (*db.Project, error) { return nil, tc.err },
			}, nil)
			_, err := svc.GetProjectMetadata(project.AuthedContext(t), connect.NewRequest(&pb.GetProjectMetadataRequest{ProjectId: "p-1"}))
			requireCode(t, err, tc.code)
		})
	}
}

// A by-id read after the ownership check: the store's miss is NotFound, an
// outage is Internal (it used to read "project not found").
func TestGetProject_StoreErrorClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range storeFailures(svcerr.NotFound("project")) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := ownedProjectRepo(t)
			store.GetProjectFunc = func(context.Context, string) (*db.Project, error) { return nil, tc.err }
			_, err := newService(t, store, nil).GetProjectMetadata(project.AuthedContext(t),
				connect.NewRequest(&pb.GetProjectMetadataRequest{ProjectId: "p-1"}))
			requireCode(t, err, tc.code)
		})
	}
}

// RemoveProjectFromDaemon treats a missing clone as done. It used to treat
// every failure of the lookup that way, so a database outage answered success
// with the clone still on disk.
func TestRemoveProjectFromDaemon_OnlyAMissNotAnOutageIsSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range storeFailures(core.ErrProjectNotFound) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newService(t, &project.StubRepo{
				GetProjectWithUserCheckFunc: func(context.Context, string, string) (*db.Project, error) { return nil, tc.err },
			}, nil)
			_, err := svc.RemoveProjectFromDaemon(project.AuthedContext(t),
				connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
			if tc.code == connect.CodeNotFound {
				require.NoError(t, err, "nothing to remove is success")
				return
			}
			requireCode(t, err, tc.code)
		})
	}
}

// A daemon command that failed: the daemon's own words (git, from the user's
// machine) reach the user; the router's and the transport's do not.
func TestProjectDaemonCommand_KeepsDaemonTextWithholdsTransportText(t *testing.T) {
	t.Parallel()
	gitProject := ownedProject()
	gitProject.IsGitRepo = true
	for name, tc := range map[string]struct {
		err      error
		code     connect.Code
		contains string
		withheld string
	}{
		"the daemon's git refusal": {
			err:  toolexec.DaemonReportedFailure("project.git_info", "fatal: not a git repository"),
			code: connect.CodeUnavailable, contains: "fatal: not a git repository",
		},
		"a transport failure": {
			err:  errors.New("nats: no responders available for request on reliant.daemon.d-1.cmd"),
			code: connect.CodeUnavailable, contains: "get git info failed", withheld: "nats",
		},
		"no machine at all": {
			err:  toolexec.ErrNoDaemon,
			code: connect.CodeFailedPrecondition, contains: "no daemon available",
		},
		"a machine too busy to answer": {
			err:  fmt.Errorf("wait: %w", toolexec.ErrDaemonTimeout),
			code: connect.CodeDeadlineExceeded,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := ownedProjectRepo(t)
			store.GetProjectFunc = func(context.Context, string) (*db.Project, error) { return gitProject, nil }
			router := &project.StubRouter{
				SendDaemonCommandFunc: func(context.Context, string, string, []byte, int32) ([]byte, error) { return nil, tc.err },
			}
			_, err := newService(t, store, router).GetProjectGitInfo(project.AuthedContext(t),
				connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1"}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "err = %v", err)
			msg := svcerr.ToConnect(err).Message()
			assert.Contains(t, msg, tc.contains)
			if tc.withheld != "" {
				assert.NotContains(t, msg, tc.withheld)
			}
		})
	}
}

// Over the wire, through a real Connect client and the svcerr interceptor:
// the find half of find-or-create keeps AlreadyExists AND the "already exists"
// marker the project picker keys on (ProjectPicker.tsx, ProjectPickerModal.tsx),
// and ListProjects' deadline is a deadline on the wire, not internal.
func TestProjectService_OnTheWire(t *testing.T) {
	t.Parallel()
	store := &project.StubRepo{
		GetProjectByPathAndUserFunc: func(context.Context, string, string) (*db.Project, error) {
			return ownedProject(), nil
		},
		ListProjectsFunc: func(context.Context, db.ProjectFilters) ([]*db.Project, error) {
			return nil, fmt.Errorf("list projects: %w", context.DeadlineExceeded)
		},
	}
	router := &project.StubRouter{
		SendDaemonCommandFunc: func(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
			if commandType != "fs.stat" {
				return nil, fmt.Errorf("unexpected command %s", commandType)
			}
			return json.Marshal(map[string]any{"exists": true})
		},
	}
	_, client := project.NewTestProjectServer(t, project.WithProjectDeps(project.Deps{Repo: store, Router: router}))

	_, err := client.CreateProject(context.Background(), connect.NewRequest(&pb.CreateProjectRequest{
		Name: "proj", Path: "/work",
	}))
	// A client's error is a *connect.Error already: svcerr reads it as it is.
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err), "err = %v", err)
	assert.Equal(t, "a project already exists at this path", svcerr.ToConnect(err).Message(),
		"the web's find-or-create keys on \"already exists\"")

	_, err = client.ListProjects(context.Background(), connect.NewRequest(&pb.ListProjectsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(err), "err = %v", err)
}
