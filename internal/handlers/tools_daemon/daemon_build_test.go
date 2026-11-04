package tools_daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

type connectedRecorder struct {
	LifecyclePublisher
	mu     sync.Mutex
	builds map[string]daemonwire.Build
}

func (r *connectedRecorder) Connected(daemonID, _, _ string, build daemonwire.Build) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builds[daemonID] = build
}
func (*connectedRecorder) Disconnected(string, string, string)     {}
func (*connectedRecorder) Activity(string, string, string, string) {}

func (r *connectedRecorder) build(daemonID string) (daemonwire.Build, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.builds[daemonID]
	return b, ok
}

func listedDaemon(t *testing.T, registry *daemon_registry.Service, userID, daemonID string) *reliantv1.DaemonInfo {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := registry.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	require.NoError(t, err)
	for _, d := range resp.Msg.GetDaemons() {
		if d.GetDaemonId() == daemonID {
			return d
		}
	}
	t.Fatalf("daemon %s not listed", daemonID)
	return nil
}

// The build a daemon registers with is stored on its lease, served by the
// registry, carried on the connected event and counted by the fleet gauge.
func TestRegisteredBuildIsStoredServedAndPublished(t *testing.T) {
	repo, rawDB, registry, userID, daemonID := leaseFixture(t)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	events := &connectedRecorder{builds: map[string]daemonwire.Build{}}
	svc.deps.StatePublisher = events

	want := daemonwire.Build{Version: "1.8.3-dev+b675d224", Commit: "b675d22417c3", CommitTimeUnix: 1792800000, ForgeVersion: "v0.1.47"}
	dialDaemonAs(t, svc, userID, &reliantv1.DaemonRegister{
		DaemonId: daemonID, Hostname: "ws-" + daemonID[:8], Platform: "linux", DaemonType: "local",
		Version: want.Version, Commit: want.Commit, CommitTimeUnix: want.CommitTimeUnix, ForgeVersion: want.ForgeVersion,
	})

	var stored db.DaemonBuild
	require.NoError(t, rawDB.QueryRow(
		`SELECT build_version, build_commit, build_commit_time_unix, build_forge_version FROM daemon_attachment WHERE daemon_id = $1`, daemonID,
	).Scan(&stored.Version, &stored.Commit, &stored.CommitTimeUnix, &stored.ForgeVersion))
	require.Equal(t, db.DaemonBuild{Version: want.Version, Commit: want.Commit, CommitTimeUnix: want.CommitTimeUnix, ForgeVersion: want.ForgeVersion}, stored)

	info := listedDaemon(t, registry, userID, daemonID)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, info.GetStatus())
	require.Equal(t, want.Version, info.GetVersion())
	require.Equal(t, want.Commit, info.GetCommit())
	require.Equal(t, want.CommitTimeUnix, info.GetCommitTimeUnix())
	require.Equal(t, want.ForgeVersion, info.GetForgeVersion())

	got, ok := events.build(daemonID)
	require.True(t, ok, "a connected event was published")
	require.Equal(t, want, got)

	fleet := svc.fleetBuilds()
	require.Len(t, fleet, 1)
	require.Equal(t, 1, fleet[0].Count)
	require.Equal(t, want.Version, fleet[0].Version)
	require.Equal(t, want.Commit, fleet[0].Commit)
}

// A daemon that predates the build fields registers as before: it connects, is
// served as an unknown build (all empty/zero), and publishes no build.
func TestDaemonWithoutBuildFieldsIsAnUnknownBuild(t *testing.T) {
	repo, _, registry, userID, daemonID := leaseFixture(t)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	events := &connectedRecorder{builds: map[string]daemonwire.Build{}}
	svc.deps.StatePublisher = events

	dialDaemon(t, svc, userID, daemonID)

	info := listedDaemon(t, registry, userID, daemonID)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, info.GetStatus())
	require.Empty(t, info.GetVersion())
	require.Empty(t, info.GetCommit())
	require.Zero(t, info.GetCommitTimeUnix())
	require.Empty(t, info.GetForgeVersion())

	got, ok := events.build(daemonID)
	require.True(t, ok)
	require.Equal(t, daemonwire.Build{}, got)
	require.False(t, got.Known())
}

// A heartbeat that finds the lease reaped re-creates it with the build the
// session registered, not an unknown one.
func TestRecreatedLeaseKeepsTheSessionsBuild(t *testing.T) {
	repo, rawDB, _, userID, daemonID := leaseFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	stream := newParkedStream()
	conn := newTestConn(userID, daemonID, stream)
	conn.build = db.DaemonBuild{Version: "1.8.3", Commit: "abc123", CommitTimeUnix: 42, ForgeVersion: "v0.1.47"}
	runIncoming(t, svc, conn, stream)

	stream.recv <- heartbeatMsg(time.Now().UnixMilli())
	require.Eventually(t, func() bool {
		var commit string
		err := rawDB.QueryRow(`SELECT build_commit FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&commit)
		return err == nil && commit == "abc123"
	}, 3*time.Second, 20*time.Millisecond, "the re-created lease must carry the session's build")
}

func TestFleetBuildsCountsDaemonsPerBuild(t *testing.T) {
	svc := newTestService(nil)
	t.Cleanup(svc.Close)
	add := func(build db.DaemonBuild) {
		conn := newTestConn("u", uuid.NewString(), newParkedStream())
		conn.build = build
		svc.mu.Lock()
		registerTestConn(svc, conn)
		svc.mu.Unlock()
	}
	add(db.DaemonBuild{Version: "1.8.3", Commit: "aaa"})
	add(db.DaemonBuild{Version: "1.8.3", Commit: "aaa"})
	add(db.DaemonBuild{Version: "1.8.2", Commit: "bbb"})
	add(db.DaemonBuild{})

	counts := map[[2]string]int{}
	for _, b := range svc.fleetBuilds() {
		counts[[2]string{b.Version, b.Commit}] = b.Count
	}
	require.Equal(t, map[[2]string]int{{"1.8.3", "aaa"}: 2, {"1.8.2", "bbb"}: 1, {"", ""}: 1}, counts)
}
