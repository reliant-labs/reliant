// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// These tests pin the gateway half of the prod outage of 2026-10-11: a cloud
// daemon whose stream stayed registered on the gateway read DISCONNECTED to
// every chat for hours while its terminal and tool calls worked (routing does
// not read the lease). Nothing was read from its stream for ~20 minutes
// (11:51-12:11 UTC); the TTL reaper deleted its daemon_attachment lease at
// 12:11:13; the stream then recovered, but the renewal every later heartbeat
// performed was an UPDATE, so the lease never came back. Throughout the stall
// the stale-connection sweeper counted the gateway's own heartbeat sends as
// liveness and never fired.
//
// The lease has exactly one writer, the gateway holding the stream: upsert on
// connect, renew on every heartbeat its receive loop reads, release — of its
// own session's row only — on teardown.

func heartbeatMsg(ts int64) *reliantv1.DaemonMessage {
	return &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Heartbeat{
		Heartbeat: &reliantv1.DaemonHeartbeat{Timestamp: ts},
	}}
}

// runIncoming serves conn's receive loop until the test ends.
func runIncoming(t *testing.T, svc *Service, conn *daemonConnection, stream *parkedStream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		_ = svc.handleIncoming(ctx, conn)
	}()
	t.Cleanup(func() {
		cancel()
		close(stream.recv)
		<-handlerDone
	})
}

// leaseProbeRepo is the real repository, reporting each lease renewal and
// detected-ports write the gateway's background goroutines finish. A test
// waits on these instead of polling the database against a wall-clock bound:
// on a loaded gate host one write can take seconds (a fresh pool connection
// to a busy shared Postgres), and the renewer and the state worker run
// independently, so polling also left the test unable to tell WHICH
// heartbeat's write it was looking at.
type leaseProbeRepo struct {
	Repository
	renewed chan bool     // one per finished RenewDaemonAttachment: whether it re-created the row
	ports   chan struct{} // one per finished UpdateDaemonAttachmentPorts
}

func newLeaseProbeRepo(repo Repository) *leaseProbeRepo {
	return &leaseProbeRepo{Repository: repo, renewed: make(chan bool, 64), ports: make(chan struct{}, 64)}
}

func (r *leaseProbeRepo) RenewDaemonAttachment(ctx context.Context, att *db.DaemonAttachment) (bool, error) {
	created, err := r.Repository.RenewDaemonAttachment(ctx, att)
	if err == nil {
		r.renewed <- created
	}
	return created, err
}

func (r *leaseProbeRepo) UpdateDaemonAttachmentPorts(ctx context.Context, daemonID string, ports []uint32) error {
	err := r.Repository.UpdateDaemonAttachmentPorts(ctx, daemonID, ports)
	if err == nil {
		r.ports <- struct{}{}
	}
	return err
}

// leaseSettle bounds the waits in this file that still poll for the
// gateway's asynchronous lease and state writes. Like awaitProbe's, it only
// keeps a broken gateway from hanging the run: the fixed 2-3s waits these used
// timed out under host load while the write was on its way
// (TestHeartbeatRestoresAMissingLeaseAndDisconnectIsStillPrompt, three
// merge-queue gates on 2026-10-26). "At once" below means without waiting out
// a lease, so the bound is held under daemon_registry.AttachmentStaleThreshold.
const leaseSettle = 30 * time.Second

// awaitProbe waits for the gateway's next finished write of one kind. The
// bound only keeps a broken gateway from hanging the run; it is not a
// latency requirement.
func awaitProbe[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for the gateway to %s", what)
		panic("unreachable")
	}
}

// A heartbeat read off a live stream must restore a lease the reaper deleted,
// tell the owner's clients — which were showing the machine disconnected — to
// re-read, and put back the detected ports the fresh row lost.
func TestHeartbeatOnALiveStreamRecreatesAReapedLease(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	userID, daemonID := "lease-user", uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	probe := newLeaseProbeRepo(repo)
	svc := newTestService(probe)
	hub := &recordingUserHub{}
	svc.deps.UserUpdateHub = hub
	stream := newParkedStream()
	conn := newTestConn(userID, daemonID, stream)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: db.DaemonAttachmentSourceInbound,
		AttachedAt: conn.attachedAt, LastStreamActivity: conn.attachedAt,
	}))
	runIncoming(t, svc, conn, stream)

	beat := func() {
		stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Heartbeat{
			Heartbeat: &reliantv1.DaemonHeartbeat{Timestamp: time.Now().UnixMilli(), DetectedPorts: []uint32{3000}},
		}}
	}
	ports := func() string {
		var p sql.NullString
		if err := rawDB.QueryRow(`SELECT detected_ports FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&p); err != nil {
			return ""
		}
		return p.String
	}

	// The first heartbeat renews the connect-time lease and records the
	// ports. Both writes must have LANDED before the reaper is simulated: a
	// renewal still in flight when the row is deleted re-creates it on the
	// first heartbeat's behalf (correctly — the stream is alive), and the
	// re-creation this test is about would then never come from the next one.
	beat()
	require.False(t, awaitProbe(t, probe.renewed, "renew the lease for the first heartbeat"),
		"the first heartbeat's renewal re-created a lease that existed")
	awaitProbe(t, probe.ports, "record the first heartbeat's detected ports")
	require.Equal(t, "[3000]", ports())

	// The reaper deletes the lease while the stream is still held (it does
	// so whenever no renewal landed for 15 minutes).
	_, err := rawDB.Exec(`DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	refetchesBefore := len(daemonListRefetches(t, hub)[userID])

	// The stream is moving again: the daemon's next heartbeat is read.
	beat()

	require.True(t, awaitProbe(t, probe.renewed, "renew the lease for the heartbeat after the reap"),
		"a heartbeat read off a live stream must re-create the lease the reaper deleted; "+
			"otherwise ListDaemons reports the daemon DISCONNECTED until it happens to reconnect")
	attached, err := repo.IsDaemonIDAttached(ctx, daemonID, daemon_registry.AttachmentStaleThreshold)
	require.NoError(t, err)
	require.True(t, attached, "the re-created lease does not read as attached")
	// Announced right after the re-creating write returns, in memory.
	require.Eventually(t, func() bool { return len(daemonListRefetches(t, hub)[userID]) == refetchesBefore+1 },
		30*time.Second, 5*time.Millisecond,
		"re-creating a lease must tell the owner's clients to re-read the daemon list, once; "+
			"they were showing the machine disconnected and nothing else would correct them before the slow fallback poll")

	// The re-created row starts with no detected ports; the next heartbeat
	// must rewrite them even though the set did not change. Exactly one of
	// the two heartbeats since the reap writes them: whichever the state
	// worker applies after the renewer flagged the re-creation.
	beat()
	awaitProbe(t, probe.ports, "rewrite the detected ports the re-created lease lost")
	require.Equal(t, "[3000]", ports(),
		"the re-created lease lost the daemon's detected ports and the unchanged set was never rewritten")
}

// leaseRecordingRepo parks state-message writes (a FileSystemChanged's project
// lookup) until released, and records lease renewals as they happen.
type leaseRecordingRepo struct {
	Repository

	stateEntered chan struct{}
	release      chan struct{}

	mu       sync.Mutex
	renewals []*db.DaemonAttachment
}

var _ Repository = (*leaseRecordingRepo)(nil)

func (r *leaseRecordingRepo) GetProjectByPathAndUser(context.Context, string, string) (*db.Project, error) {
	r.stateEntered <- struct{}{}
	<-r.release
	return nil, nil
}

func (r *leaseRecordingRepo) RenewDaemonAttachment(_ context.Context, att *db.DaemonAttachment) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renewals = append(r.renewals, att)
	return false, nil
}

func (r *leaseRecordingRepo) UpdateDaemonAttachmentPorts(context.Context, string, []uint32) error {
	return nil
}

func (r *leaseRecordingRepo) renewalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.renewals)
}

// The lease says "this stream is being read". It must not wait for the state
// worker to work through unrelated writes queued ahead of the heartbeat: when
// it rode that FIFO, a backlog of slow writes aged a live stream's lease out.
func TestLeaseRenewalIsNotQueuedBehindStatePersistence(t *testing.T) {
	repo := &leaseRecordingRepo{stateEntered: make(chan struct{}, 1), release: make(chan struct{})}
	svc := newTestService(repo)
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)
	runIncoming(t, svc, conn, stream)
	t.Cleanup(func() { close(repo.release) })

	// Park the state worker on a slow write.
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_FileSystemChanged{
		FileSystemChanged: &reliantv1.FileSystemChanged{ProjectPath: "/work/project"},
	}}
	select {
	case <-repo.stateEntered:
	case <-time.After(leaseSettle):
		t.Fatal("the gateway never started persisting the state message")
	}

	stream.recv <- heartbeatMsg(time.Now().UnixMilli())

	require.Eventually(t, func() bool { return repo.renewalCount() == 1 }, leaseSettle, 10*time.Millisecond,
		"the heartbeat's lease renewal waited behind an unrelated state write; the lease "+
			"must track the receive loop reading heartbeats, not the state worker's backlog")
	repo.mu.Lock()
	renewal := repo.renewals[0]
	repo.mu.Unlock()
	require.Equal(t, "user-1", renewal.UserID, "a renewal must carry the owner it needs to re-create the row")
	require.True(t, renewal.AttachedAt.Equal(conn.attachedAt),
		"a renewal must name its session's attached_at (got %v, want %v), or a re-created row cannot be released by its own session",
		renewal.AttachedAt, conn.attachedAt)
}

// A stream the gateway has read nothing from for longer than the threshold is
// dead, however recently the gateway itself wrote to it. The gateway's 15s
// heartbeat send succeeds into a buffer whether or not the daemon's side is
// moving, so liveness keyed on it never runs out.
func TestSweeperReapsAStreamSilentInboundWhileTheGatewayKeepsSending(t *testing.T) {
	// Teardown's lease release, without a database; the recorder keeps what
	// was released.
	repo := &MockRepository{ReleaseDaemonAttachmentFunc: func(context.Context, string, time.Time) error { return nil }}
	svc := newTestService(repo)
	t.Cleanup(svc.Close)

	base := time.Date(2026, 10, 11, 12, 10, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }

	newConn := func(lastReceived time.Time) *daemonConnection {
		return &daemonConnection{
			userID:            uuid.NewString(),
			daemonID:          uuid.NewString(),
			connectedAt:       base.Add(-9 * time.Hour),
			attachedAt:        base.Add(-9 * time.Hour),
			lastActivity:      base, // the gateway's heartbeat just went out
			lastReceived:      lastReceived,
			done:              make(chan struct{}),
			terminalSubs:      make(map[string][]chan *toolexec.TerminalOutputEvent),
			processOutputSubs: make(map[string][]chan *toolexec.ProcessOutputEvent),
		}
	}
	silent := newConn(base.Add(-19 * time.Minute))
	reading := newConn(base.Add(-10 * time.Second))

	svc.mu.Lock()
	registerTestConn(svc, silent)
	registerTestConn(svc, reading)
	svc.mu.Unlock()

	svc.sweepStaleConnections()

	require.False(t, svc.HasConnectedDaemonsForUser(silent.userID),
		"a stream silent inbound for 19 minutes must be reaped even though the gateway sent to it just now")
	select {
	case <-silent.done:
	default:
		t.Fatal("reaping must close the connection so its stream handler ends and the daemon redials")
	}
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(silent.closedReason()),
		"the stream must end with a status the daemon treats as retryable, not a bare EOF")
	var released []string
	for _, call := range repo.Calls("ReleaseDaemonAttachment") {
		released = append(released, call.Args[1].(string))
	}
	require.Equal(t, []string{silent.daemonID}, released)
	require.True(t, svc.HasConnectedDaemonsForUser(reading.userID),
		"a stream read from 10s ago is alive and must not be reaped")
}

// The gateway's keepalive acknowledges the newest daemon heartbeat it has read,
// so the daemon can tell a gateway that is consuming its stream from one that
// has stopped. Before the first daemon heartbeat it acknowledges nothing (0).
func TestServerHeartbeatAcknowledgesTheNewestDaemonHeartbeatRead(t *testing.T) {
	svc := newTestService(&leaseRecordingRepo{stateEntered: make(chan struct{}, 1), release: make(chan struct{})})
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)

	require.Zero(t, newServerHeartbeat(conn, time.Now()).GetHeartbeat().GetAckedDaemonHeartbeat())

	runIncoming(t, svc, conn, stream)
	stream.recv <- heartbeatMsg(1_000)
	stream.recv <- heartbeatMsg(2_000)

	require.Eventually(t, func() bool {
		return newServerHeartbeat(conn, time.Now()).GetHeartbeat().GetAckedDaemonHeartbeat() == 2_000
	}, leaseSettle, 5*time.Millisecond, "the keepalive must acknowledge the newest daemon heartbeat read")
}

// ---------------------------------------------------------------------------
// Across replicas: the registry's status for a daemon must follow its stream,
// whatever order the gateways' lease writes land in. These run the real
// ConnectDaemon path on gateway replicas sharing one database, with the
// registry reading it.
// ---------------------------------------------------------------------------

// dialDaemon registers daemonID as userID on svc over a real ConnectDaemon
// stream and returns it once the gateway has acknowledged the registration.
// The stream stays open until the test closes it or ends.
func dialDaemon(t *testing.T, svc *Service, userID, daemonID string) *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage] {
	t.Helper()
	return dialDaemonAs(t, svc, userID, &reliantv1.DaemonRegister{DaemonId: daemonID, Hostname: "ws-" + daemonID[:8], Platform: "linux", DaemonType: "local"})
}

// dialDaemonAs is dialDaemon with the registration the test chooses.
func dialDaemonAs(t *testing.T, svc *Service, userID string, reg *reliantv1.DaemonRegister) *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage] {
	t.Helper()
	routes := http.NewServeMux()
	svc.Register(routes) // the production mount
	// Streams.Install is the listener's edge: the abortable request body
	// ConnectDaemon needs (serverkit.AbortReceive).
	mux := serverkit.NewStreams().Install(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get("X-Test-User"))
		routes.ServeHTTP(w, r.WithContext(ctx))
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
	client := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC())
	stream := client.ConnectDaemon(ctx)
	stream.RequestHeader().Set("X-Test-User", userID)
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: reg,
	}}))
	msg, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, msg.GetRegistrationAck(), "first server message must be the registration ack")
	return stream
}

// registryStatus is what ListDaemons tells the web about daemonID.
func registryStatus(t *testing.T, registry *daemon_registry.Service, userID, daemonID string) reliantv1.DaemonStatus {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := registry.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	require.NoError(t, err)
	for _, d := range resp.Msg.GetDaemons() {
		if d.GetDaemonId() == daemonID {
			return d.GetStatus()
		}
	}
	t.Fatalf("daemon %s not listed", daemonID)
	return reliantv1.DaemonStatus_DAEMON_STATUS_UNSPECIFIED
}

func heldConn(svc *Service, daemonID string) *daemonConnection {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return svc.connections[daemonID]
}

func leaseFixture(t *testing.T) (repo *db.Repo, rawDB *sql.DB, registry *daemon_registry.Service, userID, daemonID string) {
	t.Helper()
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	return repo, rawDB, newDaemonRegistry(t, repo, nil), "lease-user-" + uuid.NewString()[:8], uuid.NewString()
}

// The old replica tearing down its half of the stream after the daemon has
// re-attached elsewhere must not take the new session's lease with it: a
// release deletes only its own session's row. Deleting by daemon id alone
// left a serving daemon DISCONNECTED until its next heartbeat re-created the
// row — and the old replica's list-changed announcement sent every client to
// read exactly that.
func TestDaemonStaysActiveWhenTheOldReplicaTearsDownAfterAReconnect(t *testing.T) {
	repo, _, registry, userID, daemonID := leaseFixture(t)

	replicaA := newTestService(repo)
	t.Cleanup(replicaA.Close)
	replicaB := newTestService(repo)
	t.Cleanup(replicaB.Close)

	dialDaemon(t, replicaA, userID, daemonID)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, registryStatus(t, registry, userID, daemonID))
	connA := heldConn(replicaA, daemonID)
	require.NotNil(t, connA)

	// The daemon re-attaches to replica B while A still holds its half —
	// a drain, or a half-open connection A's sweeper has not noticed yet.
	time.Sleep(2 * time.Millisecond) // the new session's attach is strictly later
	dialDaemon(t, replicaB, userID, daemonID)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, registryStatus(t, registry, userID, daemonID))

	// A finally tears its half down.
	replicaA.teardownConnection(connA, "test: stale sweeper on the old replica")

	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, registryStatus(t, registry, userID, daemonID),
		"the old replica's teardown deleted the lease of the session now serving on another replica")
}

// The fence must not cost the ordinary case: a lease a heartbeat re-created is
// still this session's, and a real disconnect releases it at once rather than
// leaving a serving-looking lease to expire.
func TestHeartbeatRestoresAMissingLeaseAndDisconnectIsStillPrompt(t *testing.T) {
	repo, rawDB, registry, userID, daemonID := leaseFixture(t)

	gateway := newTestService(repo)
	t.Cleanup(gateway.Close)
	stream := dialDaemon(t, gateway, userID, daemonID)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE, registryStatus(t, registry, userID, daemonID))

	// The lease disappears while the stream lives.
	_, err := rawDB.Exec(`DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	require.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED, registryStatus(t, registry, userID, daemonID))

	require.NoError(t, stream.Send(heartbeatMsg(time.Now().UnixMilli())))
	require.Eventually(t, func() bool {
		return registryStatus(t, registry, userID, daemonID) == reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE
	}, leaseSettle, 20*time.Millisecond,
		"the daemon's keepalive did not restore its missing lease; the registry keeps reporting a serving daemon as DISCONNECTED")

	// The daemon goes away for real: reported without waiting out a lease.
	// The wait must be too short for a lease to expire, or it proves nothing.
	require.Less(t, leaseSettle, daemon_registry.AttachmentStaleThreshold/2)
	require.NoError(t, stream.CloseRequest())
	require.Eventually(t, func() bool {
		return registryStatus(t, registry, userID, daemonID) == reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED
	}, leaseSettle, 20*time.Millisecond, "a real disconnect must still release the (re-created) lease at once")
}

// livenessDownRepo lists the user's daemon but cannot read the lease table.
type livenessDownRepo struct {
	daemon_registry.Repository
	daemon *db.Daemon
}

var _ daemon_registry.Repository = livenessDownRepo{}

func (r livenessDownRepo) ListDaemonsByUserID(context.Context, string) ([]*db.Daemon, error) {
	return []*db.Daemon{r.daemon}, nil
}

func (r livenessDownRepo) GetDaemon(context.Context, string) (*db.Daemon, error) {
	return r.daemon, nil
}

func (livenessDownRepo) ListFreshDaemonAttachmentsForUser(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
	return nil, errors.New("pq: canceling statement due to statement timeout")
}

func (livenessDownRepo) ListDaemonLocalModels(context.Context, string) (map[string]string, error) {
	return nil, nil
}

// A registry that cannot read liveness must say so, not report every one of
// the user's machines DISCONNECTED: the client cannot tell that answer from
// the truth, keeps it, and tells the user their machine dropped. An error lets
// it keep the status it last knew and blame the connection to Reliant.
func TestRegistryReportsUnavailableRatherThanDisconnectedWhenLivenessCannotBeRead(t *testing.T) {
	selfHosted := "self_hosted"
	daemon := &db.Daemon{ID: uuid.NewString(), UserID: "u-1", DaemonType: &selfHosted}
	registry := newDaemonRegistry(t, livenessDownRepo{daemon: daemon}, nil)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "u-1")

	resp, err := registry.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err == nil {
		t.Fatalf("ListDaemons answered %v for a daemon whose liveness it could not read", resp.Msg.GetDaemons()[0].GetStatus())
	}
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err))

	_, err = registry.GetDaemon(ctx, connect.NewRequest(&reliantv1.GetDaemonRequest{DaemonId: daemon.ID}))
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}
