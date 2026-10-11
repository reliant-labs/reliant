// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// These tests pin the gateway half of the prod outage of 2026-10-11: a cloud
// daemon whose stream stayed registered on the gateway for nine hours read
// DISCONNECTED to every chat for the last six of them. Nothing was read from
// its stream for ~20 minutes (11:51-12:11 UTC); the TTL reaper deleted its
// daemon_attachment lease at 12:11:13; the stream then recovered, but the
// renewal every later heartbeat performed was an UPDATE, so the lease never
// came back. And throughout the stall the stale-connection sweeper — the one
// component meant to notice a stream gone quiet — counted the gateway's own
// heartbeat sends as liveness and never fired.

func heartbeatMsg(ts int64) *reliantv1.DaemonMessage {
	return &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Heartbeat{
		Heartbeat: &reliantv1.DaemonHeartbeat{Timestamp: ts},
	}}
}

// runIncoming serves conn's receive loop until the test ends.
func runIncoming(t *testing.T, svc *ToolsDaemonService, conn *daemonConnection, stream *parkedStream) {
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

// A heartbeat read off a live stream must restore a lease the reaper deleted.
func TestHeartbeatOnALiveStreamRecreatesAReapedLease(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	userID, daemonID := "lease-user", uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: db.DaemonAttachmentSourceInbound,
	}))

	svc := NewToolsDaemonService(repo)
	stream := newParkedStream()
	conn := newTestConn(userID, daemonID, stream)
	runIncoming(t, svc, conn, stream)

	// The reaper deletes the lease while the stream is still held (it does
	// so whenever no renewal landed for 15 minutes).
	require.NoError(t, repo.DeleteDaemonAttachment(ctx, daemonID))

	// The stream is moving again: the daemon's next heartbeat is read.
	stream.recv <- heartbeatMsg(time.Now().UnixMilli())

	require.Eventually(t, func() bool {
		attached, err := repo.IsDaemonIDAttached(ctx, daemonID, daemonAttachmentStaleThreshold)
		return err == nil && attached
	}, 3*time.Second, 20*time.Millisecond,
		"a heartbeat read off a live stream must re-create the lease the reaper deleted; "+
			"otherwise ListDaemons reports the daemon DISCONNECTED until it happens to reconnect")
}

// leaseRecordingRepo parks state-message writes (a FileSystemChanged's project
// lookup) until released, and records lease renewals as they happen.
type leaseRecordingRepo struct {
	db.Repository

	stateEntered chan struct{}
	release      chan struct{}

	mu       sync.Mutex
	renewals []*db.DaemonAttachment
}

func (r *leaseRecordingRepo) GetProjectByPathAndUser(context.Context, string, string) (*db.Project, error) {
	r.stateEntered <- struct{}{}
	<-r.release
	return nil, nil
}

func (r *leaseRecordingRepo) RenewDaemonAttachment(_ context.Context, att *db.DaemonAttachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renewals = append(r.renewals, att)
	return nil
}

func (r *leaseRecordingRepo) TouchDaemonAttachmentIfNewer(_ context.Context, daemonID string, at time.Time) error {
	// The pre-fix renewal path. Recorded the same way so this test measures
	// WHEN a renewal happens, not which method performed it.
	return r.RenewDaemonAttachment(context.Background(), &db.DaemonAttachment{DaemonID: daemonID, LastStreamActivity: at})
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
	svc := NewToolsDaemonService(repo)
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
	case <-time.After(2 * time.Second):
		t.Fatal("the gateway never started persisting the state message")
	}

	stream.recv <- heartbeatMsg(time.Now().UnixMilli())

	require.Eventually(t, func() bool { return repo.renewalCount() == 1 }, 2*time.Second, 10*time.Millisecond,
		"the heartbeat's lease renewal waited behind an unrelated state write; the lease "+
			"must track the receive loop reading heartbeats, not the state worker's backlog")
	repo.mu.Lock()
	renewal := repo.renewals[0]
	repo.mu.Unlock()
	require.Equal(t, "user-1", renewal.UserID, "a renewal must carry the owner it needs to re-create the row")
}

// deleteRecordingRepo satisfies teardown's lease delete without a database.
type deleteRecordingRepo struct {
	db.Repository
	mu      sync.Mutex
	deleted []string
}

func (r *deleteRecordingRepo) DeleteDaemonAttachment(_ context.Context, daemonID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, daemonID)
	return nil
}

// A stream the gateway has read nothing from for longer than the threshold is
// dead, however recently the gateway itself wrote to it. The gateway's 15s
// heartbeat send succeeds into a buffer whether or not the daemon's side is
// moving, so liveness keyed on it never runs out.
func TestSweeperReapsAStreamSilentInboundWhileTheGatewayKeepsSending(t *testing.T) {
	repo := &deleteRecordingRepo{}
	svc := NewToolsDaemonService(repo)
	t.Cleanup(svc.Close)

	base := time.Date(2026, 10, 11, 12, 10, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }

	newConn := func(lastReceived time.Time) *daemonConnection {
		return &daemonConnection{
			userID:            uuid.NewString(),
			daemonID:          uuid.NewString(),
			connectedAt:       base.Add(-9 * time.Hour),
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
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(silent.closedReason()),
		"the stream must end with a status the daemon treats as retryable, not a bare EOF")
	require.Equal(t, []string{silent.daemonID}, repo.deleted)
	require.True(t, svc.HasConnectedDaemonsForUser(reading.userID),
		"a stream read from 10s ago is alive and must not be reaped")
}

// The gateway's keepalive acknowledges the newest daemon heartbeat it has read,
// so the daemon can tell a gateway that is consuming its stream from one that
// has stopped. Before the first daemon heartbeat it acknowledges nothing (0).
func TestServerHeartbeatAcknowledgesTheNewestDaemonHeartbeatRead(t *testing.T) {
	svc := NewToolsDaemonService(&leaseRecordingRepo{stateEntered: make(chan struct{}, 1), release: make(chan struct{})})
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)

	require.Zero(t, newServerHeartbeat(conn, time.Now()).GetHeartbeat().GetAckedDaemonHeartbeat())

	runIncoming(t, svc, conn, stream)
	stream.recv <- heartbeatMsg(1_000)
	stream.recv <- heartbeatMsg(2_000)

	require.Eventually(t, func() bool {
		return newServerHeartbeat(conn, time.Now()).GetHeartbeat().GetAckedDaemonHeartbeat() == 2_000
	}, 2*time.Second, 5*time.Millisecond, "the keepalive must acknowledge the newest daemon heartbeat read")
}
