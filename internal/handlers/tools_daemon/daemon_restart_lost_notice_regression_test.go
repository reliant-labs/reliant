package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The regression also compiles against the original schema: absence of the
// process field must fail preservation, never turn into a build-only "proof".
func TestRestartLostNoticeDurableIntentCannotDisappear(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id, incarnation := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: "owner"}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: id, UserID: "owner", Source: db.DaemonAttachmentSourceInbound, AttachedAt: at, LastStreamActivity: at}))
	intent, err := json.Marshal(map[string]any{"deadline_unix_ms": time.Now().Add(time.Minute).UnixMilli(), "reason": "notice queued then disconnected", "incarnation_id": incarnation})
	require.NoError(t, err)
	require.NoError(t, repo.SetDaemonRestartStatus(ctx, "owner", id, at, string(intent)))
	conn := newTestConn("owner", id, newParkedStream())
	conn.attachedAt = at
	reg := &pb.DaemonRegister{}
	if field := reg.ProtoReflect().Descriptor().Fields().ByName("incarnation_id"); field != nil {
		reg.ProtoReflect().Set(field, protoreflect.ValueOfString(incarnation))
	}
	svc := newTestService(repo)
	require.NoError(t, svc.restoreRestart(ctx, conn, reg))
	row, err := repo.GetDaemon(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, row.RestartStatus, "same process reconnect after lost notice must not erase durable intent")
	require.ErrorIs(t, svc.sendToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "raced-new"}}}), toolexec.ErrDaemonUpdating)
	select {
	case msg := <-conn.sendCh:
		require.NotNil(t, msg.GetRestartRequested())
	default:
		t.Fatal("accepted restart notice was not resent")
	}
}
