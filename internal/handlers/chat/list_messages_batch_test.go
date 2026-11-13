package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"
)

// ListMessages used to fetch content blocks with three separate
// single-message ListContentBlocks calls per message (attachment scan,
// tool-result map, render). It now batches all messages' blocks in a single
// ListContentBlocksForMessages call and groups them in memory. This test
// asserts the assembled output — block order, tool-call/tool-result
// pairing, and attachments — is unchanged by that switch. A single-message
// fixture would not catch the sqlc IN-clause defect (matches only the first
// id), so this fixture has several messages.
func TestListMessages_BatchedContentBlocks_MatchesPerMessageAssembly(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "batch assembly",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	// Message 1: user text.
	msg1ID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msg1ID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msg1ID, Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("please check the weather"),
		CreatedAt: now, UpdatedAt: now,
	}))

	// Message 2: assistant with a text block followed by a tool_call block
	// (two blocks, ordered by position — the batched path must preserve
	// per-message ordering).
	msg2ID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()
	toolName := "get_weather"
	toolInput := `{"city":"nyc"}`
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msg2ID, ChatID: chatID, Ordinal: 2, Seq: 2, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msg2ID, Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("let me check"),
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msg2ID, Position: 1,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID, ToolName: &toolName, ToolInput: &toolInput,
		CreatedAt: now, UpdatedAt: now,
	}))

	// Message 3: TOOL role with the matching tool_result block. This is
	// what ToolResultsByCallID must pair with message 2's tool_call block.
	msg3ID := uuid.New().String()
	resultContent := `{"temp":72}`
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msg3ID, ChatID: chatID, Ordinal: 3, Seq: 3, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msg3ID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolCallID: &toolCallID, Content: &resultContent,
		CreatedAt: now, UpdatedAt: now,
	}))

	// Message 4: assistant final text, no blocks in common with the others —
	// just padding to confirm the batch covers >2 messages cleanly.
	msg4ID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msg4ID, ChatID: chatID, Ordinal: 4, Seq: 4, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msg4ID, Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("it's 72F in NYC"),
		CreatedAt: now, UpdatedAt: now,
	}))

	// The RPC span the interceptor would start, recorded, so the transcript
	// byte accounting ListMessages puts on it can be read back.
	recorder := tracetest.NewSpanRecorder()
	spanCtx, rpcSpan := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).
		Tracer("test").Start(ctx, "ListMessages")

	svc := &Service{deps: Deps{Repo: repo}}
	resp, err := svc.ListMessages(spanCtx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID,
	}))
	rpcSpan.End()
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 4, "all four messages must be present")

	require.Len(t, recorder.Ended(), 1)
	attrs := map[attribute.Key]int64{}
	for _, kv := range recorder.Ended()[0].Attributes() {
		attrs[kv.Key] = kv.Value.AsInt64()
	}
	var wireBytes int64
	for _, m := range resp.Msg.Messages {
		wireBytes += int64(proto.Size(m))
	}
	assert.Equal(t, int64(4), attrs["transcript.messages"])
	assert.Equal(t, wireBytes, attrs["transcript.bytes"])
	assert.Equal(t, int64(5), attrs["transcript.blocks"])
	assert.Equal(t, int64(3), attrs["transcript.text_blocks"])
	assert.Equal(t, int64(len("please check the weather")+len("let me check")+len("it's 72F in NYC")), attrs["transcript.text_bytes"])
	assert.Equal(t, int64(len(toolInput)), attrs["transcript.tool_input_bytes"])
	assert.Equal(t, int64(len(resultContent)), attrs["transcript.tool_result_bytes"])
	assert.Equal(t, int64(0), attrs["transcript.thinking_bytes"])
	assert.Contains(t, attrs, attribute.Key("transcript.other_bytes"))

	// Order and identity preserved.
	assert.Equal(t, msg1ID, resp.Msg.Messages[0].Id)
	assert.Equal(t, msg2ID, resp.Msg.Messages[1].Id)
	assert.Equal(t, msg3ID, resp.Msg.Messages[2].Id)
	assert.Equal(t, msg4ID, resp.Msg.Messages[3].Id)

	// Message 1: single text block.
	require.Len(t, resp.Msg.Messages[0].ContentBlocks, 1)
	assert.Equal(t, "please check the weather", *resp.Msg.Messages[0].ContentBlocks[0].Content)

	// Message 2: two blocks in position order, tool_call paired with its result.
	require.Len(t, resp.Msg.Messages[1].ContentBlocks, 2)
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, resp.Msg.Messages[1].ContentBlocks[0].Type)
	toolCallBlock := resp.Msg.Messages[1].ContentBlocks[1]
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, toolCallBlock.Type)
	require.NotNil(t, toolCallBlock.ToolCallId)
	assert.Equal(t, toolCallID, *toolCallBlock.ToolCallId)
	assert.Nil(t, toolCallBlock.Content, "the result is not copied onto its call: the client joins them by tool_call_id")

	// Message 3: the tool_result block carries the result, under the same
	// tool_call_id the call block names.
	require.Len(t, resp.Msg.Messages[2].ContentBlocks, 1)
	resultBlock := resp.Msg.Messages[2].ContentBlocks[0]
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, resultBlock.Type)
	require.NotNil(t, resultBlock.ToolCallId)
	assert.Equal(t, toolCallID, *resultBlock.ToolCallId)
	require.NotNil(t, resultBlock.Content)
	assert.Equal(t, resultContent, *resultBlock.Content)

	// Message 4: single text block, unrelated to the tool exchange.
	require.Len(t, resp.Msg.Messages[3].ContentBlocks, 1)
	assert.Equal(t, "it's 72F in NYC", *resp.Msg.Messages[3].ContentBlocks[0].Content)
}
