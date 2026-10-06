// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cpdaemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// recordedRegistry is the slice of reliant's own daemon registry the router
// reads: the user's daemon rows and which of them hold a fresh attachment.
type recordedRegistry struct {
	db.Repository
	daemons  []*db.Daemon
	attached []string
}

func (r *recordedRegistry) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for _, d := range r.daemons {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *recordedRegistry) ListAttachedDaemonIDsForUser(context.Context, string, time.Duration) ([]string, error) {
	return r.attached, nil
}

// fakeControlPlane serves what control-plane's admin-server actually serves
// for daemons — controlplane.v1.DaemonService — and answers every other path
// the way the real server does: 404. Requests to any other path are recorded,
// so a test can prove nothing still dials the route control-plane deleted
// (/reliant.v1.DaemonRegistryService/*, see control-plane's MountExtraRoutes).
type fakeControlPlane struct {
	controlplanev1connect.UnimplementedDaemonServiceHandler

	mu           sync.Mutex
	resumes      []string // daemon ids
	resumeAuth   []string // Authorization headers
	strayPaths   []string // every request to a path this server does not serve
	notSuspended bool     // answer ResumeDaemon as the real one does for a daemon it no longer holds suspended
}

func (f *fakeControlPlane) ResumeDaemon(_ context.Context, req *connect.Request[cpdaemonv1.ResumeDaemonRequest]) (*connect.Response[cpdaemonv1.ResumeDaemonResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, req.Msg.GetDaemonId())
	f.resumeAuth = append(f.resumeAuth, req.Header().Get("Authorization"))
	if f.notSuspended {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}
	return connect.NewResponse(&cpdaemonv1.ResumeDaemonResponse{}), nil
}

func startFakeControlPlane(t *testing.T) (*fakeControlPlane, string) {
	t.Helper()
	cp := &fakeControlPlane{}
	path, handler := controlplanev1connect.NewDaemonServiceHandler(cp)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		cp.strayPaths = append(cp.strayPaths, r.URL.Path)
		cp.mu.Unlock()
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return cp, srv.URL
}

func lifecycle(phase string) *string { return &phase }

// The attended-send wake, end to end through the api-server's real router
// wiring: a suspended managed daemon (as recorded in reliant's own registry)
// is resumed through controlplane.v1.DaemonService/ResumeDaemon, as the user.
//
// This is the path that always failed in production with "control plane
// ResolveDaemon: unimplemented: 404 Not Found": the router asked control-plane
// for a reliant.v1 route control-plane no longer serves, so it never reached
// a resume at all.
func TestAPIServerRouterWakesSuspendedDaemonThroughControlPlaneDaemonService(t *testing.T) {
	const userID = "user-wake-wiring"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	cp, cpURL := startFakeControlPlane(t)
	repo := &recordedRegistry{daemons: []*db.Daemon{{
		ID: "daemon-suspended", UserID: userID, LifecyclePhase: lifecycle("suspended"),
	}}}
	router := toolexec.NewNATSDaemonRouter(nil, daemonRouterOptions(repo, cpURL)...)

	id, err := router.EnsureAwake(context.Background(), userID, &toolexec.DaemonSelector{ID: "daemon-suspended"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-suspended", id)
	assert.Equal(t, []string{"daemon-suspended"}, cp.resumes, "exactly one resume, for the suspended daemon")
	assert.Equal(t, []string{"Bearer jwt-attended"}, cp.resumeAuth, "the resume acts as the signed-in user")
	assert.Empty(t, cp.strayPaths, "the router must call only routes control-plane serves")
}

// A daemon the registry records as provisioning is "please wait", not "no
// daemon": ErrDaemonPending, answered from reliant's own records with no
// request to anyone.
func TestAPIServerRouterReportsProvisioningDaemonAsPendingFromItsOwnRecords(t *testing.T) {
	cp, cpURL := startFakeControlPlane(t)
	repo := &recordedRegistry{daemons: []*db.Daemon{{
		ID: "daemon-provisioning", UserID: "user-pending-wiring", LifecyclePhase: lifecycle("provisioning"),
	}}}
	router := toolexec.NewNATSDaemonRouter(nil, daemonRouterOptions(repo, cpURL)...)

	_, err := router.ResolveDaemonID(context.Background(), "user-pending-wiring")
	require.Error(t, err)
	assert.True(t, toolexec.IsDaemonPending(err), "got: %v", err)
	assert.Empty(t, cp.strayPaths, "resolution reads reliant's own registry; it must not dial control-plane")
	assert.Empty(t, cp.resumes, "resolution never wakes")
}

// Resolving a routable daemon costs no HTTP hop: the old wiring paid a failing
// round trip to control-plane on every resolution that missed the local
// resolver.
func TestAPIServerRouterResolvesAttachedDaemonWithoutDialingControlPlane(t *testing.T) {
	cp, cpURL := startFakeControlPlane(t)
	repo := &recordedRegistry{
		daemons:  []*db.Daemon{{ID: "daemon-live", UserID: "user-live-wiring"}},
		attached: []string{"daemon-live"},
	}
	router := toolexec.NewNATSDaemonRouter(nil, daemonRouterOptions(repo, cpURL)...)

	id, err := router.ResolveDaemonID(context.Background(), "user-live-wiring")
	require.NoError(t, err)
	assert.Equal(t, "daemon-live", id)
	assert.Empty(t, cp.strayPaths)
}

// The registry mirrors lifecycle from control-plane over NATS, so it can lag a
// resume that already happened (a second message sent seconds after the
// first). control-plane then refuses with FailedPrecondition "daemon is not
// suspended" — the daemon is already waking, which is what the caller wanted.
func TestAPIServerRouterTreatsAlreadyResumedDaemonAsAwake(t *testing.T) {
	const userID = "user-already-waking"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	cp, cpURL := startFakeControlPlane(t)
	cp.notSuspended = true
	repo := &recordedRegistry{daemons: []*db.Daemon{{
		ID: "daemon-lagging", UserID: userID, LifecyclePhase: lifecycle("suspended"),
	}}}
	router := toolexec.NewNATSDaemonRouter(nil, daemonRouterOptions(repo, cpURL)...)

	id, err := router.EnsureAwake(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-lagging", id)
	assert.Equal(t, []string{"daemon-lagging"}, cp.resumes)
	assert.Empty(t, cp.strayPaths)
}
