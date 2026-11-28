package db_test

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestMessageContextCompatibility(t *testing.T) {
	t.Parallel()
	origin := core.MessageOrigin{ThreadID: "thread", Subject: "hello"}
	fromOld, ok := core.MessageOriginFrom(db.WithMessageOrigin(context.Background(), origin))
	require.True(t, ok)
	require.Equal(t, pb.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE, fromOld.Kind)
	fromNew, ok := db.MessageOriginFrom(core.WithMessageOrigin(context.Background(), origin))
	require.True(t, ok)
	require.Equal(t, fromOld, fromNew)
	require.True(t, core.LLMOnlyDelivery(db.WithLLMOnlyDelivery(context.Background())))
	ctx := core.WithLLMOnlyDelivery(context.Background())
	require.True(t, db.LLMOnlyDelivery(ctx))
	require.Equal(t, core.QueuedDisplayStyle(ctx), db.QueuedDisplayStyle(ctx))
}

func TestMessageUpdatePayloadCompatibility(t *testing.T) {
	t.Parallel()
	zero := 0
	attachments := []map[string]interface{}{}
	payload := core.MessageUpdateData{UpdateType: "message", ID: "m", Role: int32(1), Seq: 2, Ordinal: 3, Thread: "t", ContentBlocks: []map[string]interface{}{}, CreatedAt: "now", ContextSequence: &zero, Attachments: &attachments, Sender: core.NewMessageSenderPayload("sender", nil, pb.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE, "")}
	got, err := payload.Marshal()
	require.NoError(t, err)
	require.JSONEq(t, `{"update_type":"message","id":"m","role":1,"seq":2,"ordinal":3,"thread":"t","content_blocks":[],"created_at":"now","context_sequence":0,"attachments":[],"sender":{"thread_id":"sender","kind":1}}`, got)
	oldBytes, err := json.Marshal(db.MessageUpdateData(payload))
	require.NoError(t, err)
	require.JSONEq(t, got, string(oldBytes))
}
