// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// CreateProjectFromRepo replaces a four-call client chain (ListGitRepos ->
// CloneRepo -> CreateProject -> MarkProjectInstalled) whose steps each failed
// independently, so a failure mid-sequence left a project with no checkout or
// a checkout with no project. These pin the two properties that made the
// chain worth replacing: nothing is persisted when the clone is refused, and
// a queued clone is recorded AS queued rather than as a finished one.

// cloneStubControlPlane records what the service asked the control plane to
// do, and replays a canned result.
type cloneStubControlPlane struct {
	result  controlplane.CloneRepoResult
	err     error
	calls   int
	lastReq controlplane.CloneRepoRequest
	lastJWT string
}

func (s *cloneStubControlPlane) CloneRepoOntoDaemon(
	_ context.Context, jwt string, req controlplane.CloneRepoRequest,
) (controlplane.CloneRepoResult, error) {
	s.calls++
	s.lastReq = req
	s.lastJWT = jwt
	if s.err != nil {
		return controlplane.CloneRepoResult{}, s.err
	}
	return s.result, nil
}

var _ CloneDispatcher = (*cloneStubControlPlane)(nil)

func newFromRepoService(t *testing.T, cp *cloneStubControlPlane) (*Service, *db.Repo, func()) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	svc := NewTestProject(t, WithProjectDeps(Deps{
		Repo:         repo,
		Router:       &createProjectRowRouter{resolveID: "daemon-abc"},
		ControlPlane: cp,
	}))
	return svc, repo, cleanup
}

func TestCreateProjectFromRepo_QueuedCloneIsRecordedAsInstalling(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{
		ClonedPath: "/home/workspace/projects/widgets",
		Queued:     true,
		DaemonName: "my-machine",
	}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	// The whole point: the checkout does not exist yet, and the response
	// must not imply otherwise.
	assert.True(t, resp.Msg.GetQueued(), "a queued clone must report queued=true")
	assert.Equal(t, "my-machine", resp.Msg.GetDaemonName(),
		"the UI needs the machine's name to say what it is waiting on")
	require.NotNil(t, resp.Msg.GetProject())
	assert.Equal(t, reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING,
		resp.Msg.GetProjectDaemon().GetInstallState())

	// And it is persisted that way, so a page reload still shows the truth.
	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "installing", string(rows[0].InstallState))
	assert.NotEmpty(t, rows[0].InstallRequestID,
		"without a request id the outcome can never be matched back and the project sits in installing forever")
}

func TestCreateProjectFromRepo_RefusedClonePersistsNothing(t *testing.T) {
	// The reason this is one RPC: a refused clone must not leave a project
	// row the user has to clean up by hand.
	cp := &cloneStubControlPlane{err: connect.NewError(
		connect.CodeFailedPrecondition, errors.New(`no git credential found for provider "github"`))}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.Error(t, err)
	// The control plane's own code reaches the user, rather than being
	// flattened to Internal — "connect GitHub" is actionable, "internal
	// error" is not.
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))

	projects, err := repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	assert.Empty(t, projects, "a refused clone must not leave a project behind")
}

// TestCreateProjectFromRepo_DeletedMachinePersistsNothing pins the exact prod
// failure of 2026-10-07 14:38:49: the clone was dispatched to cda8a89b, a
// machine the control plane had deleted the day before, and came back
// not_found. Nothing may be persisted, and NotFound must reach the client
// unflattened — it is what tells the UI the machine is gone, so it can drop
// it as a target and say so.
func TestCreateProjectFromRepo_DeletedMachinePersistsNothing(t *testing.T) {
	cp := &cloneStubControlPlane{err: connect.NewError(
		connect.CodeNotFound, errors.New(`daemon "cda8a89b-d15c-455d-a506-4c40335e4278" not found`))}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/reliant-labs/forge.git",
		DaemonId: "cda8a89b-d15c-455d-a506-4c40335e4278",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	projects, err := repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	assert.Empty(t, projects, "a clone dispatched to a deleted machine must not leave a project behind")
}

func TestCreateProjectFromRepo_CompletedCloneIsRecordedAsInstalled(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{
		ClonedPath: "/home/workspace/projects/widgets",
		Queued:     false,
	}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetQueued())

	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "installed", string(rows[0].InstallState))
}

func TestCreateProjectFromRepo_RequiresATargetMachine(t *testing.T) {
	// Not defaulted server-side on purpose: with several machines the choice
	// is the user's, and picking one silently puts the checkout somewhere
	// they did not ask for.
	cp := &cloneStubControlPlane{}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-x")
	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets.git",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Zero(t, cp.calls, "must not dispatch a clone with no target")
}

func TestCreateProjectFromRepo_ForwardsTheCallersJWT(t *testing.T) {
	// reliant has no GitHub token of its own — the control plane holds it,
	// scoped to the calling user. Forwarding the caller's own credential is
	// what keeps the clone authorized as THEM.
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	req := connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets-" + uuid.NewString() + ".git",
		DaemonId: "daemon-abc",
	})
	req.Header().Set("Authorization", "Bearer caller-jwt-123")

	_, err := svc.CreateProjectFromRepo(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "caller-jwt-123", cp.lastJWT)
}

func TestCreateProjectFromRepo_DerivesNameAndPathFromTheCloneURL(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	assert.Equal(t, name, resp.Msg.GetProject().GetName(),
		"the repo name is the obvious default; making the user retype it is friction")
	assert.Equal(t, "/home/workspace/projects/"+name, cp.lastReq.Path)
}

// A clone URL that carries a token (pasted by a user, or built by a client)
// is cleaned before it goes anywhere: the control plane clones the clean URL
// and authenticates through the credential helper, and the project stores the
// clean URL rather than a token at rest.
func TestCreateProjectFromRepo_NeverForwardsOrStoresACredentialURL(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	name := "widgets-" + uuid.NewString()

	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://x-access-token:ghs_FIXTURE0TOKEN@github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	want := "https://github.com/acme/" + name + ".git"
	assert.Equal(t, want, cp.lastReq.CloneURL, "the control plane is handed the clean URL")
	assert.Equal(t, want, resp.Msg.GetProject().GetRemoteUrl(), "the response carries the clean URL")
	stored, err := repo.GetProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	require.NotNil(t, stored.RemoteURL)
	assert.Equal(t, want, *stored.RemoteURL, "the stored remote is the clean URL")
}

// TestCreateProjectFromRepo_FailedQueuedCloneIsMarkedFailed is the whole round
// trip of a queued clone that fails. The daemon's DaemonCommandFailed echoes
// the request id the queued command carried, and that is the ONLY key the
// failure path has to find the project's install row. The control plane used
// to mint its own timestamp id while reliant recorded "clone:<project>:<daemon>",
// so the two never matched: a failed clone left the project "installing"
// forever. Now the id reliant records is the id it hands the control plane.
func TestCreateProjectFromRepo_FailedQueuedCloneIsMarkedFailed(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets-" + uuid.NewString() + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	projectID := resp.Msg.GetProject().GetId()

	require.NotEmpty(t, cp.lastReq.RequestID, "the clone must carry a request id the failure can be matched by")
	rows, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, cp.lastReq.RequestID, rows[0].InstallRequestID,
		"the id handed to the control plane must be the id the install row is keyed by")

	// The daemon runs the queued command and it fails. Its announcement
	// carries the command's request id — the one the control plane stamped.
	daemon := dialToolsDaemon(t, repo, userID, "daemon-abc")
	require.NoError(t, daemon.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandFailed{
		DaemonCommandFailed: &reliantv1.DaemonCommandFailed{
			RequestId:    cp.lastReq.RequestID,
			CommandType:  "git.clone",
			ErrorMessage: "remote: Repository not found.",
		},
	}}))

	require.Eventually(t, func() bool {
		rows, err = repo.ListProjectDaemonsForProject(ctx, projectID)
		return err == nil && len(rows) == 1 && string(rows[0].InstallState) == "failed"
	}, 5*time.Second, 20*time.Millisecond, "the gateway must mark the failed clone's install row failed")
	assert.Contains(t, rows[0].InstallError, "Repository not found")
}

// dialToolsDaemon plays a daemon: it registers daemonID as userID on a real
// daemon gateway (tools_daemon) over ConnectDaemon and returns the stream
// once the gateway has acknowledged the registration, for the test to send
// what a daemon sends.
func dialToolsDaemon(t *testing.T, repo tools_daemon.Repository, userID, daemonID string) *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage] {
	t.Helper()
	gateway, err := tools_daemon.New(tools_daemon.Deps{
		Logger: slog.New(slog.DiscardHandler),
		Config: &typedconfig.Config{},
		Repo:   repo,
	})
	require.NoError(t, err)
	t.Cleanup(gateway.Close)

	routes := http.NewServeMux()
	gateway.Register(routes) // the production mount
	// A serverkit listener gives every request an abortable body; ConnectDaemon
	// refuses a stream without one.
	mux := serverkit.NewStreams().Install(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.UserIDContextKey, userID)))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC()).ConnectDaemon(ctx)
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{DaemonId: daemonID, Hostname: "ws-" + daemonID, Platform: "linux", DaemonType: "local"},
	}}))
	msg, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, msg.GetRegistrationAck(), "first server message must be the registration ack")
	return stream
}

// TestCreateProjectFromRepo_StampsCreationTime pins the timestamps. The
// projects store writes created_at / updated_at / last_active verbatim and the
// columns have no default, so a project added from a repo was dated
// 0001-01-01 — the owner's project in prod carries exactly that.
func TestCreateProjectFromRepo_StampsCreationTime(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	before := time.Now().Add(-time.Minute)
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets-" + uuid.NewString() + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	stored, err := repo.GetProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	for field, ts := range map[string]time.Time{
		"created_at":  stored.CreatedAt,
		"updated_at":  stored.UpdatedAt,
		"last_active": stored.LastActive,
	} {
		assert.True(t, ts.After(before), "%s = %s, want the time the project was added", field, ts)
	}
}

func TestRepoNameFromCloneURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/widgets.git":  "widgets",
		"https://github.com/acme/widgets":      "widgets",
		"https://github.com/acme/widgets/":     "widgets",
		"git@github.com:acme/widgets.git":      "widgets",
		"https://github.com/acme/dots.in.name": "dots.in.name",
	} {
		assert.Equal(t, want, repoNameFromCloneURL(url), "clone url %q", url)
	}
}
