// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// stalePathRouter answers fs.stat with a configurable "does the directory
// exist" verdict, and can fail every daemon command to model an unreachable
// daemon. It stands in for the workspace filesystem the API tier cannot see.
type stalePathRouter struct {
	toolexectest.Stub
	placesAsNamed
	dirExists    bool
	daemonDown   bool
	statCommands int
}

func (r *stalePathRouter) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	return "daemon-stale-path", nil
}

func (r *stalePathRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *stalePathRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	if r.daemonDown {
		return nil, assertDaemonDown
	}
	switch commandType {
	case "fs.stat":
		r.statCommands++
		return json.Marshal(map[string]any{"exists": r.dirExists, "is_dir": r.dirExists})
	case "repo.discover":
		return json.Marshal(map[string]any{"discovered": []any{}})
	default:
		return json.Marshal(map[string]any{})
	}
}

func (r *stalePathRouter) EnqueueDaemonCommand(_ context.Context, _, _ string, _ []byte, _ int32) (int, error) {
	return 1, nil
}

var assertDaemonDown = &daemonDownError{}

type daemonDownError struct{}

func (*daemonDownError) Error() string { return "no daemon connected" }

// seedProject creates a project row the way a first-ever open would, then
// returns its id. The router used here reports the directory as present.
func seedProject(t *testing.T, repo *db.Repo, ctx context.Context, path string) string {
	t.Helper()
	// The machine the create ran on is the user's: its row records the
	// checkout, and the find branch asks that machine about the path.
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: "daemon-stale-path", UserID: auth.MustGetUserID(ctx)}))
	s := newProjectService(t, repo, &stalePathRouter{dirExists: true})
	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "stale-path-project",
		Path: path,
	}))
	require.NoError(t, err)
	return resp.Msg.GetProject().GetId()
}

// TestCreateProject_RefusesStaleProjectRowWhoseDirectoryIsGone is the
// find-or-create half that was never wired: CreateProject mkdirs on the CREATE
// branch, but the FIND branch returned AlreadyExists without ever asking
// whether the directory still exists. A projects row that outlives its
// directory then binds a run to a phantom path — the run starts, executes
// nothing, and reports success.
//
// A row is not a directory. When the daemon says the directory is gone, the
// resolution must fail loudly and name the path, BEFORE any activity executes.
func TestCreateProject_RefusesStaleProjectRowWhoseDirectoryIsGone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-stale-project-path"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	projectPath := "/home/workspace/projects/stale-" + uuid.New().String()

	projectID := seedProject(t, repo, ctx, projectPath)

	// The directory is deleted out from under the row; the row survives.
	router := &stalePathRouter{dirExists: false}
	s := newProjectService(t, repo, router)

	_, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "stale-path-project",
		Path: projectPath,
	}))
	require.Error(t, err, "resolving a project whose directory is gone must not succeed")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err),
		"a stale registry row is a precondition failure, not AlreadyExists")
	assert.Contains(t, err.Error(), projectPath, "the refusal must name the missing directory")
	assert.Contains(t, err.Error(), projectID, "the refusal must name the project row to act on")
	assert.Positive(t, router.statCommands, "the find branch must actually ask the daemon")
}

// TestCreateProject_AcceptsExistingProjectWhoseDirectoryExists is the control:
// the ordinary repeat-open still reports AlreadyExists, which every
// find-or-create caller treats as "found".
func TestCreateProject_AcceptsExistingProjectWhoseDirectoryExists(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-live-project-path"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	projectPath := "/home/workspace/projects/live-" + uuid.New().String()

	seedProject(t, repo, ctx, projectPath)

	s := newProjectService(t, repo, &stalePathRouter{dirExists: true})
	_, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "stale-path-project",
		Path: projectPath,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
}

// TestCreateProject_UnreachableDaemonDoesNotRefuse guards the boundary of the
// refusal: only a daemon that ANSWERED "does not exist" is evidence of a stale
// row. An unreachable daemon is a different failure — the onboarding flow
// legitimately races daemon provisioning by ~10s — and must not be reported as
// a missing project directory.
func TestCreateProject_UnreachableDaemonDoesNotRefuse(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-project-path-daemon-down"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	projectPath := "/home/workspace/projects/downdaemon-" + uuid.New().String()

	seedProject(t, repo, ctx, projectPath)

	s := newProjectService(t, repo, &stalePathRouter{daemonDown: true})
	_, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "stale-path-project",
		Path: projectPath,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err),
		"an unreachable daemon must fall through to the normal found answer")
	if strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, must not claim the directory is missing when the daemon never answered", err)
	}
}

// statByMachineRouter answers fs.stat per machine, and refuses default
// resolution: the find branch must ask a named machine.
type statByMachineRouter struct {
	toolexectest.Stub
	placesAsNamed
	exists map[string]bool
	asked  []string
}

func (r *statByMachineRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, fmt.Errorf("%s sent by default resolution: the path is the requesting machine's", commandType)
}

func (r *statByMachineRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	if commandType == "fs.stat" {
		r.asked = append(r.asked, daemonID)
		return json.Marshal(map[string]any{"exists": r.exists[daemonID]})
	}
	return json.Marshal(map[string]any{})
}

// The find half of find-or-create asks about the caller's path on the
// machine the request runs on. A user whose default machine is A, running
// with --project-path on B (where the project's checkout is recorded), was
// told the directory did not exist because A was asked.
func TestCreateProject_FindAsksTheMachineTheRequestRunsOn(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-find-machine-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	projectPath := "/home/workspace/projects/find-" + uuid.NewString()
	projectID := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "find", Path: projectPath}))
	for _, id := range []string{"machine-a", "machine-b"} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id + userID, UserID: userID}))
	}
	a, b := "machine-a"+userID, "machine-b"+userID
	require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, b, projectPath, nil))

	router := &statByMachineRouter{exists: map[string]bool{a: false, b: true}}
	_, err := newProjectService(t, repo, router).CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "find", Path: projectPath}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err), "found on B, where its checkout is: err = %v", err)
	assert.Equal(t, []string{b}, router.asked)

	// A request on A's own credentials asks A, which says the path is gone.
	router.asked = nil
	onA := context.WithValue(ctx, auth.DaemonIDContextKey, a)
	_, err = newProjectService(t, repo, router).CreateProject(onA, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "find", Path: projectPath}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, []string{a}, router.asked)
}
