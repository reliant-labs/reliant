// Copyright (c) 2026 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The chat stream under TOOL_BODY_MODE_STUB (dev-docs/design/
// chat-lazy-tool-bodies.md): the snapshot stubs finished calls' bodies, a
// replay stubs results and thinking but keeps inputs, and a live frame is
// always full. Unset is FULL, byte for byte.

// fakeChatUpdates is a ChatUpdateSubscriber whose events the test sends.
type fakeChatUpdates struct {
	ch chan streaming.UpdateEvent[db.ChatUpdate]
}

func (f *fakeChatUpdates) Subscribe(context.Context, string) streaming.UpdateSubscription[db.ChatUpdate] {
	return f
}
func (f *fakeChatUpdates) Events() <-chan streaming.UpdateEvent[db.ChatUpdate] { return f.ch }
func (f *fakeChatUpdates) Unsubscribe()                                        {}

type toolBodiesStreamFixture struct {
	dbtest.TranscriptBodies
	// The chat's updates, in order: a filler, then one MESSAGE update per
	// message, carrying the blocks as the live writers store them.
	updates []db.ChatUpdate
}

func seedToolBodiesStream(t *testing.T, ctx context.Context, repo *db.Repo) toolBodiesStreamFixture {
	t.Helper()
	f := toolBodiesStreamFixture{TranscriptBodies: dbtest.SeedTranscriptBodies(t, ctx, repo, raceTestUserID)}
	require.NoError(t, repo.CreateChatUpdate(ctx, f.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL, "filler", `{}`))
	for _, msgID := range []string{f.AssistantMessageID, f.ToolMessageID} {
		blocks, err := repo.ListContentBlocks(ctx, msgID)
		require.NoError(t, err)
		data, err := json.Marshal(map[string]any{
			"id": msgID, "chat_id": f.ChatID, "content_blocks": db.ContentBlockPayloadsWithToolCalls(blocks, nil),
		})
		require.NoError(t, err)
		require.NoError(t, repo.CreateChatUpdate(ctx, f.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, msgID, string(data)))
	}
	updates, err := repo.GetUpdatesSince(ctx, f.ChatID, 0, 0)
	require.NoError(t, err)
	require.Len(t, updates, 3)
	f.updates = updates
	return f
}

// updateBlocks decodes a MESSAGE update's {message: {content_blocks}} by id.
func updateBlocks(t *testing.T, dataJSON string) map[string]map[string]any {
	t.Helper()
	var payload struct {
		Message struct {
			ContentBlocks []map[string]any `json:"content_blocks"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(dataJSON), &payload))
	byID := map[string]map[string]any{}
	for _, b := range payload.Message.ContentBlocks {
		byID[b["id"].(string)] = b
	}
	return byID
}

func snapshotBlocks(snapshot *reliantv1.ChatSyncSnapshot) map[string]*reliantv1.ContentBlock {
	byID := map[string]*reliantv1.ContentBlock{}
	for _, m := range snapshot.Messages {
		for _, b := range m.ContentBlocks {
			byID[b.Id] = b
		}
	}
	return byID
}

func TestChatStreamToolBodies(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, raceTestUserID)
	f := seedToolBodiesStream(t, ctx, repo)

	t.Run("unset is FULL, byte for byte: snapshot and update frames", func(t *testing.T) {
		svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo}))
		chat, err := repo.GetChatRow(ctx, f.ChatID)
		require.NoError(t, err)
		marshal := func(mode reliantv1.ToolBodyMode) []byte {
			snapshot, _, err := svc.buildChatSnapshot(ctx, chat, mode)
			require.NoError(t, err)
			b, err := proto.MarshalOptions{Deterministic: true}.Marshal(&reliantv1.ChatSyncSnapshot{Messages: snapshot.Messages})
			require.NoError(t, err)
			return b
		}
		assert.Equal(t, marshal(reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED), marshal(reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL))

		snapshot, _, err := svc.buildChatSnapshot(ctx, chat, reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED)
		require.NoError(t, err)
		blocks := snapshotBlocks(snapshot)
		require.Contains(t, blocks, f.DoneCallBlockID)
		assert.Equal(t, f.DoneInput, blocks[f.DoneCallBlockID].GetInput())
		assert.Equal(t, f.Thinking, blocks[f.ThinkingBlockID].GetContent())
		for id, b := range blocks {
			assert.Nil(t, b.BodyElided, id)
		}

		for _, u := range f.updates {
			assert.True(t, proto.Equal(
				chatUpdateToProto(u, reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED),
				chatUpdateToProto(u, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL)), u.EntityID)
			assert.NotContains(t, chatUpdateToProto(u, reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED).DataJson, "body_elided")
		}
	})

	t.Run("STUB: the snapshot stubs, a live frame stays full", func(t *testing.T) {
		live := &fakeChatUpdates{ch: make(chan streaming.UpdateEvent[db.ChatUpdate], 1)}
		svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, ChatUpdates: live}))
		events := chatStreamEvents(t, svc, &reliantv1.StreamChatUpdatesRequest{
			ChatId: f.ChatID, ToolBodies: reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum(),
		})

		snapshot := nextChatEvent(t, events).GetSnapshot()
		require.NotNil(t, snapshot)
		blocks := snapshotBlocks(snapshot)
		done := blocks[f.DoneCallBlockID]
		assert.True(t, done.GetBodyElided())
		assert.Equal(t, int64(len(f.DoneInput)), done.GetBodySize())
		assert.NotEqual(t, f.DoneInput, done.GetInput())
		assert.Equal(t, f.RunningInput, blocks[f.RunningCallBlockID].GetInput(), "an in-flight call is whole")
		assert.Nil(t, blocks[f.ThinkingBlockID].Content)
		assert.True(t, blocks[f.ResultBlockID].GetBodyElided())
		require.NotNil(t, nextChatEvent(t, events).GetCaughtUp())

		assistant := f.updates[1]
		assistant.SequenceNumber = snapshot.LatestSequence + 1
		live.ch <- streaming.UpdateEvent[db.ChatUpdate]{Key: f.ChatID, SequenceNumber: assistant.SequenceNumber, Payload: assistant}
		batch := nextChatEvent(t, events).GetUpdates()
		require.NotNil(t, batch)
		require.Len(t, batch.Updates, 1)
		frame := updateBlocks(t, batch.Updates[0].DataJson)
		assert.Equal(t, f.DoneInput, frame[f.DoneCallBlockID]["input"], "live frames are FULL")
		assert.Equal(t, f.Thinking, frame[f.ThinkingBlockID]["content"])
		assert.NotContains(t, batch.Updates[0].DataJson, "body_elided")
	})

	t.Run("STUB: a replay stubs results and thinking, keeps inputs", func(t *testing.T) {
		svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo}))
		events := chatStreamEvents(t, svc, &reliantv1.StreamChatUpdatesRequest{
			ChatId: f.ChatID, SinceSeq: f.updates[0].SequenceNumber,
			ToolBodies: reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum(),
		})
		batch := nextChatEvent(t, events).GetUpdates()
		require.NotNil(t, batch)
		require.Len(t, batch.Updates, 2)

		assistant := updateBlocks(t, batch.Updates[0].DataJson)
		assert.Equal(t, f.DoneInput, assistant[f.DoneCallBlockID]["input"], "a replay cannot tell finished from running, so inputs stay whole")
		assert.NotContains(t, assistant[f.ThinkingBlockID], "content")
		assert.Equal(t, true, assistant[f.ThinkingBlockID]["body_elided"])

		result := updateBlocks(t, batch.Updates[1].DataJson)[f.ResultBlockID]
		assert.Equal(t, true, result["body_elided"])
		assert.Equal(t, true, result["truncated"])
		assert.LessOrEqual(t, len(result["content"].(string)), messageproto.StubResultPreviewBytes)
	})

	t.Run("the kill switch serves STUB as FULL", func(t *testing.T) {
		svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Config: &config.Config{Transcript: &configv1.TranscriptConfig{ToolBodyStubs: "off"}}}))
		events := chatStreamEvents(t, svc, &reliantv1.StreamChatUpdatesRequest{
			ChatId: f.ChatID, ToolBodies: reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum(),
		})
		snapshot := nextChatEvent(t, events).GetSnapshot()
		require.NotNil(t, snapshot)
		blocks := snapshotBlocks(snapshot)
		assert.Equal(t, f.DoneInput, blocks[f.DoneCallBlockID].GetInput())
		for id, b := range blocks {
			assert.Nil(t, b.BodyElided, id)
		}
	})
}

// nextChatEvent is the next frame that is not a heartbeat.
func nextChatEvent(t *testing.T, events <-chan *reliantv1.ChatStreamEvent) *reliantv1.ChatStreamEvent {
	t.Helper()
	for {
		select {
		case e, ok := <-events:
			require.True(t, ok, "the stream ended")
			if e.GetHeartbeat() != nil {
				continue
			}
			return e
		case <-time.After(20 * time.Second):
			t.Fatal("no frame")
			return nil
		}
	}
}
