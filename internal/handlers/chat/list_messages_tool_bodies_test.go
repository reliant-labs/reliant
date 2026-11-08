package chat

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/pkg/config"
)

// stubsOffConfig throws the kill switch: RELIANT_TRANSCRIPT_TOOL_BODY_STUBS=off.
func stubsOffConfig() *config.Config {
	return &config.Config{Transcript: &configv1.TranscriptConfig{ToolBodyStubs: "off"}}
}

// ListMessages and GetChatUpdates under ToolBodyMode
// (dev-docs/design/chat-lazy-tool-bodies.md). Unset must stay exactly what
// every client before the field got (v1); STUB leaves finished calls' bodies
// out.

func blocksByID(msgs []*reliantv1.Message) map[string]*reliantv1.ContentBlock {
	byID := map[string]*reliantv1.ContentBlock{}
	for _, m := range msgs {
		for _, b := range m.ContentBlocks {
			byID[b.Id] = b
		}
	}
	return byID
}

func deterministic(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	require.NoError(t, err)
	return b
}

func assertStubbed(t *testing.T, f dbtest.TranscriptBodies, blocks map[string]*reliantv1.ContentBlock) {
	t.Helper()
	done := blocks[f.DoneCallBlockID]
	require.NotNil(t, done)
	assert.True(t, done.GetBodyElided(), "a finished call's large input is elided")
	assert.Equal(t, int64(len(f.DoneInput)), done.GetBodySize())
	assert.NotEqual(t, f.DoneInput, done.GetInput())
	var skeleton map[string]any
	require.NoError(t, json.Unmarshal([]byte(done.GetInput()), &skeleton))
	assert.Equal(t, "internal/messageproto/stub.go", skeleton["file_path"], "the skeleton still labels the call")

	assert.Equal(t, f.RunningInput, blocks[f.RunningCallBlockID].GetInput(), "an in-flight call is whole")
	assert.Nil(t, blocks[f.RunningCallBlockID].BodyElided)
	assert.Equal(t, f.SmallInput, blocks[f.SmallCallBlockID].GetInput(), "a small input is whole")
	assert.Equal(t, f.PlanInput, blocks[f.PlanCallBlockID].GetInput(), "an always-full tool is whole")

	assert.Nil(t, blocks[f.ThinkingBlockID].Content)
	assert.True(t, blocks[f.ThinkingBlockID].GetBodyElided())
	assert.Equal(t, int64(len(f.Thinking)), blocks[f.ThinkingBlockID].GetBodySize())

	result := blocks[f.ResultBlockID]
	assert.True(t, result.GetBodyElided())
	assert.True(t, result.GetTruncated())
	assert.Equal(t, int64(len(f.Result)), result.GetOriginalSize())
	assert.LessOrEqual(t, len(result.GetContent()), messageproto.StubResultPreviewBytes)

	assert.Equal(t, "Editing the stub.", blocks[f.TextBlockID].GetContent())
}

func TestListMessagesToolBodies(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
	svc := &Service{deps: Deps{Repo: repo}}

	list := func(t *testing.T, svc *Service, req *reliantv1.ListMessagesRequest) *reliantv1.ListMessagesResponse {
		t.Helper()
		resp, err := svc.ListMessages(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg
	}
	stub := reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum()

	t.Run("unset is FULL, byte for byte, with every input whole", func(t *testing.T) {
		for _, base := range []*reliantv1.ListMessagesRequest{
			{ChatId: f.ChatID},
			{ChatId: f.ChatID, ThreadId: ptr.Of(f.ThreadID)},
			{ChatId: f.ChatID, Recent: ptr.Of(int32(1)), BeforeSeq: ptr.Of(int64(2))},
		} {
			unset := list(t, svc, base)
			withFull := proto.Clone(base).(*reliantv1.ListMessagesRequest)
			withFull.ToolBodies = reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum()
			assert.Equal(t, deterministic(t, unset), deterministic(t, list(t, svc, withFull)))

			blocks := blocksByID(unset.Messages)
			require.Contains(t, blocks, f.DoneCallBlockID)
			assert.Equal(t, f.DoneInput, blocks[f.DoneCallBlockID].GetInput())
			if b, ok := blocks[f.ThinkingBlockID]; ok {
				assert.Equal(t, f.Thinking, b.GetContent())
			}
			for id, b := range blocks {
				assert.Nil(t, b.BodyElided, id)
				assert.Nil(t, b.BodySize, id)
			}
		}
	})

	t.Run("STUB elides finished bodies", func(t *testing.T) {
		assertStubbed(t, f, blocksByID(list(t, svc, &reliantv1.ListMessagesRequest{ChatId: f.ChatID, ToolBodies: stub}).Messages))
	})

	t.Run("STUB on a thread read", func(t *testing.T) {
		resp := list(t, svc, &reliantv1.ListMessagesRequest{ChatId: f.ChatID, ThreadId: ptr.Of(f.ThreadID), ToolBodies: stub})
		assertStubbed(t, f, blocksByID(resp.Messages))
	})

	t.Run("STUB on a scroll-back page", func(t *testing.T) {
		resp := list(t, svc, &reliantv1.ListMessagesRequest{
			ChatId: f.ChatID, Recent: ptr.Of(int32(1)), BeforeSeq: ptr.Of(int64(2)), ToolBodies: stub,
		})
		require.Len(t, resp.Messages, 1)
		blocks := blocksByID(resp.Messages)
		assert.True(t, blocks[f.DoneCallBlockID].GetBodyElided())
		assert.True(t, blocks[f.ThinkingBlockID].GetBodyElided())
	})

	t.Run("the span counts what a STUB read really sent, and what it left out", func(t *testing.T) {
		recorder := tracetest.NewSpanRecorder()
		spanCtx, rpcSpan := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).
			Tracer("test").Start(ctx, "ListMessages")
		resp, err := svc.ListMessages(spanCtx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.ChatID, ToolBodies: stub}))
		rpcSpan.End()
		require.NoError(t, err)
		require.Len(t, recorder.Ended(), 1)
		attrs := map[attribute.Key]attribute.Value{}
		for _, kv := range recorder.Ended()[0].Attributes() {
			attrs[kv.Key] = kv.Value
		}

		var wire, inputs, elided int64
		for _, m := range resp.Msg.Messages {
			wire += int64(proto.Size(m))
			for _, b := range m.ContentBlocks {
				if b.GetType() == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
					inputs += int64(len(b.GetInput()))
				}
				if b.GetBodyElided() {
					elided++
				}
			}
		}
		assert.Equal(t, "TOOL_BODY_MODE_STUB", attrs["transcript.tool_bodies"].AsString())
		assert.Equal(t, wire, attrs["transcript.bytes"].AsInt64(), "bytes as sent, after stubbing")
		assert.Equal(t, inputs, attrs["transcript.tool_input_bytes"].AsInt64(), "skeletons, not stored inputs")
		assert.Equal(t, int64(0), attrs["transcript.thinking_bytes"].AsInt64())
		assert.Equal(t, elided, attrs["transcript.blocks_elided"].AsInt64())
		assert.Equal(t, int64(3), elided, "the done call's input, its result, the thinking")
		assert.Greater(t, attrs["transcript.bytes_elided"].AsInt64(), int64(len(f.Thinking)))
	})

	t.Run("the kill switch serves STUB as FULL", func(t *testing.T) {
		off := &Service{deps: Deps{Repo: repo, Config: stubsOffConfig()}}
		assert.Equal(t,
			deterministic(t, list(t, svc, &reliantv1.ListMessagesRequest{ChatId: f.ChatID})),
			deterministic(t, list(t, off, &reliantv1.ListMessagesRequest{ChatId: f.ChatID, ToolBodies: stub})))
	})
}

func TestGetChatUpdatesToolBodies(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
	for _, msgID := range []string{f.AssistantMessageID, f.ToolMessageID} {
		blocks, err := repo.ListContentBlocks(ctx, msgID)
		require.NoError(t, err)
		data, err := json.Marshal(map[string]any{"id": msgID, "content_blocks": db.ContentBlockPayloadsWithToolCalls(blocks, nil)})
		require.NoError(t, err)
		require.NoError(t, repo.CreateChatUpdate(ctx, f.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, msgID, string(data)))
	}
	svc := &Service{deps: Deps{Repo: repo}}
	get := func(t *testing.T, svc *Service, mode *reliantv1.ToolBodyMode) *reliantv1.GetChatUpdatesResponse {
		t.Helper()
		resp, err := svc.GetChatUpdates(ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{ChatId: f.ChatID, ToolBodies: mode}))
		require.NoError(t, err)
		return resp.Msg
	}

	unset := get(t, svc, nil)
	assert.Equal(t, deterministic(t, unset), deterministic(t, get(t, svc, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum())))
	require.Len(t, unset.Updates, 2)
	for _, u := range unset.Updates {
		assert.NotContains(t, u.Data, "body_elided")
	}
	assert.Contains(t, unset.Updates[0].Data, f.DoneCallBlockID)

	stubbed := get(t, svc, reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum())
	assert.Contains(t, stubbed.Updates[0].Data, `"body_elided":true`, "thinking is elided")
	var tool struct {
		ContentBlocks []map[string]any `json:"content_blocks"`
	}
	require.NoError(t, json.Unmarshal([]byte(stubbed.Updates[1].Data), &tool))
	assert.Equal(t, true, tool.ContentBlocks[0]["body_elided"])

	off := &Service{deps: Deps{Repo: repo, Config: stubsOffConfig()}}
	assert.Equal(t, deterministic(t, unset), deterministic(t, get(t, off, reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum())))
}
