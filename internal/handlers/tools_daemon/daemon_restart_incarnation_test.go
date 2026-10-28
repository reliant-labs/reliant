package tools_daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

type restartRestoreBarrierRepo struct {
	Repository
	entered chan struct{}
	release chan struct{}
}

func (r *restartRestoreBarrierRepo) GetDaemon(ctx context.Context, id string) (*db.Daemon, error) {
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return r.Repository.GetDaemon(ctx, id)
}

func TestRestartRegistrationAdmissionClosedUntilDurableRestore(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id, incarnation := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: "restore-owner"}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: id, UserID: "restore-owner", Source: db.DaemonAttachmentSourceInbound, AttachedAt: at, LastStreamActivity: at}))
	intent := &pb.DaemonRestartStatus{IncarnationId: incarnation, DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli(), Reason: "notice lost"}
	data, err := json.Marshal(intent)
	require.NoError(t, err)
	require.NoError(t, repo.SetDaemonRestartStatus(ctx, "restore-owner", id, at, string(data)))
	barrier := &restartRestoreBarrierRepo{Repository: repo, entered: make(chan struct{}), release: make(chan struct{})}
	svc := newTestService(barrier)
	ready := make(chan struct{})
	go func() {
		stream := dialDaemonAs(t, svc, "restore-owner", &pb.DaemonRegister{DaemonId: id, IncarnationId: incarnation, Hostname: "restore-host"})
		msg, err := stream.Receive()
		require.NoError(t, err)
		require.NotNil(t, msg.GetRestartRequested())
		require.Equal(t, intent.DeadlineUnixMs, msg.GetRestartRequested().DeadlineUnixMs)
		close(ready)
	}()
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("registration never entered durable restore")
	}
	conn := heldConn(svc, id)
	require.NotNil(t, conn)
	raced := make(chan error, 1)
	go func() {
		raced <- svc.sendToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "raced-new-work"}}})
	}()
	require.NotNil(t, conn.restart.Load(), "registration slot must start closed, not briefly advertise new work")
	require.Empty(t, conn.sendCh)
	close(barrier.release)
	select {
	case err := <-raced:
		require.ErrorIs(t, err, toolexec.ErrDaemonUpdating)
	case <-time.After(3 * time.Second):
		t.Fatal("racing admission did not finish")
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("same-process reconnect failed to resend lost restart notice")
	}
	require.Equal(t, incarnation, conn.restart.Load().IncarnationId)
	require.Equal(t, intent.DeadlineUnixMs, conn.restart.Load().DeadlineUnixMs)
}

func TestRestartLostNoticeSurvivesSameIncarnationAndReplacementIsFenced(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const owner = "restart-owner"
	const id = "restart-incarnation"
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: owner}))
	svc := newTestService(repo)
	at := time.Now().UTC().Truncate(time.Microsecond)
	attach := func(inc string, when time.Time) *daemonConnection {
		require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: id, UserID: owner, Source: db.DaemonAttachmentSourceInbound, AttachedAt: when, LastStreamActivity: when}))
		conn := newTestConn(owner, id, newParkedStream())
		conn.incarnationID = inc
		conn.attachedAt = when
		svc.connections[id] = conn
		return conn
	}
	incarnation := uuid.NewString()
	first := attach(incarnation, at)
	accepted, err := svc.RequestDaemonRestart(ctx, owner, id, &pb.RestartRequested{Reason: "approved replacement"})
	require.NoError(t, err)
	require.Equal(t, incarnation, accepted.IncarnationId)
	// Notice is queued, then the stream drops BEFORE the process receives it.
	require.Len(t, first.sendCh, 1)
	first.closeDone()
	same := attach(incarnation, at.Add(time.Second))
	same.restart.Store(&pb.DaemonRestartStatus{})
	require.NoError(t, svc.restoreRestart(ctx, same, &pb.DaemonRegister{IncarnationId: incarnation}))
	require.Equal(t, accepted.DeadlineUnixMs, same.restart.Load().DeadlineUnixMs)
	require.Equal(t, accepted.DeadlineUnixMs, (<-same.sendCh).GetRestartRequested().DeadlineUnixMs)
	require.ErrorIs(t, svc.sendToConn(same, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "new"}}}), toolexec.ErrDaemonUpdating)
	persisted, err := repo.GetDaemon(ctx, id)
	require.NoError(t, err)
	originalIntent := persisted.RestartStatus
	var decoded pb.DaemonRestartStatus
	require.NoError(t, json.Unmarshal([]byte(originalIntent), &decoded))
	require.Equal(t, incarnation, decoded.IncarnationId)
	// Neither a missing identity nor a stale attachment can erase the intent.
	missing := newTestConn(owner, id, newParkedStream())
	missing.attachedAt = same.attachedAt
	require.Error(t, svc.restoreRestart(ctx, missing, &pb.DaemonRegister{}))
	require.ErrorIs(t, svc.restoreRestart(ctx, first, &pb.DaemonRegister{IncarnationId: uuid.NewString()}), sql.ErrNoRows)
	replacement := attach(uuid.NewString(), at.Add(2*time.Second))
	replacement.restart.Store(&pb.DaemonRestartStatus{})
	require.NoError(t, svc.restoreRestart(ctx, replacement, &pb.DaemonRegister{IncarnationId: replacement.incarnationID}))
	require.Nil(t, replacement.restart.Load())
	row, err := repo.GetDaemon(ctx, id)
	require.NoError(t, err)
	require.Empty(t, row.RestartStatus)
	// Old process observations cannot reintroduce its intent after replacement.
	require.ErrorIs(t, svc.persistRestart(ctx, same, accepted), sql.ErrNoRows)
	// A racing newer intent cannot be cleared by a prior registration snapshot.
	// Missing identity is rejected before touching an existing lease/intent.
	_, err = svc.RegisterOutboundConnection(ctx, owner, id, "127.0.0.1", 1, &pb.DaemonRegister{}, nil, nil)
	require.Error(t, err)
	// A daemon observation for a different process never substitutes another
	// incarnation's status, even if the deadline matches.
	replacement.restart.Store(&pb.DaemonRestartStatus{IncarnationId: replacement.incarnationID, DeadlineUnixMs: accepted.DeadlineUnixMs})
	require.NoError(t, svc.recordRestart(ctx, replacement, &pb.DaemonRestartStatus{IncarnationId: incarnation, DeadlineUnixMs: accepted.DeadlineUnixMs, Drained: true}))
	require.False(t, replacement.restart.Load().Drained)
	newer := &pb.DaemonRestartStatus{IncarnationId: replacement.incarnationID, DeadlineUnixMs: accepted.DeadlineUnixMs + 1}
	require.NoError(t, svc.persistRestart(ctx, replacement, newer))
	require.ErrorIs(t, repo.CompareDaemonRestartStatus(ctx, owner, id, replacement.attachedAt, "", ""), sql.ErrNoRows)
	foreign := newTestConn("another-owner", id, newParkedStream())
	foreign.attachedAt = replacement.attachedAt
	require.Error(t, svc.restoreRestart(ctx, foreign, &pb.DaemonRegister{IncarnationId: uuid.NewString()}))
	row, err = repo.GetDaemon(ctx, id)
	require.NoError(t, err)
	require.Contains(t, row.RestartStatus, replacement.incarnationID)
}
