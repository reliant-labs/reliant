package daemon_registry

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestRequestDaemonRestartRequiresAdminAndUsesDaemonOwner(t *testing.T) {
	calls := 0
	svc, err := New(Deps{Logger: slog.New(slog.DiscardHandler), Config: &config.Config{}, Repo: &MockRepository{
		GetDaemonFunc: func(context.Context, string) (*db.Daemon, error) {
			return &db.Daemon{ID: "machine", UserID: "owner"}, nil
		},
	}, Router: &MockRouter{RequestDaemonRestartFunc: func(_ context.Context, user, daemon string, req *pb.RestartRequested) (*pb.DaemonRestartStatus, error) {
		calls++
		require.Equal(t, "owner", user)
		require.Equal(t, "machine", daemon)
		require.Equal(t, "manual", req.Reason)
		return &pb.DaemonRestartStatus{Reason: req.Reason, DeadlineUnixMs: req.DeadlineUnixMs}, nil
	}}})
	require.NoError(t, err)
	req := connect.NewRequest(&pb.RequestDaemonRestartRequest{DaemonId: "machine", Reason: "manual"})
	_, err = svc.RequestDaemonRestart(context.Background(), req)
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err))
	_, err = svc.RequestDaemonRestart(auth.WithUser(context.Background(), "owner", "authenticated", ""), req)
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
	require.Zero(t, calls)
	_, err = svc.RequestDaemonRestart(auth.WithUser(context.Background(), "admin", "admin", ""), req)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestDaemonRegistryReportsUpdatingThroughDisconnectButNotForever(t *testing.T) {
	status := &pb.DaemonRestartStatus{DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli(), Reason: "test", Drained: true}
	data, err := json.Marshal(status)
	require.NoError(t, err)
	daemonType := "self_hosted"
	daemon := &db.Daemon{ID: "machine", DaemonType: &daemonType, RestartStatus: string(data)}
	require.Equal(t, pb.DaemonStatus_DAEMON_STATUS_UPDATING, daemonToProto(daemon, nil).Status)
	require.True(t, daemonToProto(daemon, nil).RestartStatus.Drained)
	status.DeadlineUnixMs = time.Now().Add(-toolexec.DaemonRestartBudget - time.Minute).UnixMilli()
	data, err = json.Marshal(status)
	require.NoError(t, err)
	daemon.RestartStatus = string(data)
	require.Equal(t, pb.DaemonStatus_DAEMON_STATUS_DISCONNECTED, daemonToProto(daemon, nil).Status)
	daemon.RestartStatus = ""
	require.Equal(t, pb.DaemonStatus_DAEMON_STATUS_ACTIVE, daemonToProto(daemon, &db.DaemonAttachment{}).Status)
}
