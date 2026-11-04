package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// toolResultFixture writes a chat for userID with one thread, one context
// window and a TOOL message holding a tool_result for toolCallID. parentCW,
// when set, makes the window a fork of that window (a branch).
type toolResultFixture struct {
	chatID, threadID, cwID string
}

func seedToolResult(t *testing.T, repo *db.Repo, userID, toolCallID, content string, parentCW *string) toolResultFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	f := toolResultFixture{chatID: uuid.New().String(), threadID: uuid.New().String(), cwID: uuid.New().String()}

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: f.chatID, Title: "tool result", ProjectID: "test-project", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: f.threadID, ChatID: f.chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: f.cwID, ThreadID: f.threadID, ParentContextWindowID: parentCW, CreatedAt: now})
	require.NoError(t, err)

	if toolCallID == "" {
		return f
	}
	msgID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msgID, ChatID: f.chatID, Ordinal: 1, Seq: 1, ThreadID: f.threadID,
		ContextWindowID: f.cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolCallID: ptr.Of(toolCallID), ToolName: ptr.Of("shell"),
		Content: ptr.Of(content), IsError: ptr.Of(true),
		CreatedAt: now, UpdatedAt: now,
	}))
	return f
}

func TestGetToolResult(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	svc := &Service{deps: Deps{Repo: repo}}
	as := func(userID string) context.Context {
		return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	}

	// Large enough that every read path sends a preview of it.
	full := strings.Repeat("a line of a very long build log\n", 4000)
	require.Greater(t, len(full), messageproto.ToolResultPreviewBytes)

	// call_0 is the kind of id a local provider reuses in every chat: the
	// owner's chat and a stranger's chat both hold one.
	const reused = "call_0"
	mine := seedToolResult(t, repo, "test-user", reused, full, nil)
	theirs := seedToolResult(t, repo, "someone-else", reused, "the other user's output", nil)

	t.Run("returns the full stored content to the chat's owner", func(t *testing.T) {
		resp, err := svc.GetToolResult(as("test-user"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: mine.chatID, ToolCallId: reused,
		}))
		require.NoError(t, err)
		assert.Equal(t, full, resp.Msg.Content, "the whole result, not the preview")
		assert.Equal(t, int64(len(full)), resp.Msg.Size)
		assert.True(t, resp.Msg.IsError)
		assert.Equal(t, reused, resp.Msg.ToolCallId)
	})

	t.Run("a reused call id never reads another chat's result", func(t *testing.T) {
		resp, err := svc.GetToolResult(as("someone-else"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: theirs.chatID, ToolCallId: reused,
		}))
		require.NoError(t, err)
		assert.Equal(t, "the other user's output", resp.Msg.Content)
	})

	t.Run("another user's chat is not found", func(t *testing.T) {
		_, err := svc.GetToolResult(as("someone-else"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: mine.chatID, ToolCallId: reused,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("an unknown call id is not found", func(t *testing.T) {
		_, err := svc.GetToolResult(as("test-user"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: mine.chatID, ToolCallId: "call_missing",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("a call id from another of the caller's chats, not inherited, is not found", func(t *testing.T) {
		unrelated := seedToolResult(t, repo, "test-user", "", "", nil)
		_, err := svc.GetToolResult(as("test-user"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: unrelated.chatID, ToolCallId: reused,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("a branch reads the result it inherited from its source chat", func(t *testing.T) {
		branch := seedToolResult(t, repo, "test-user", "", "", ptr.Of(mine.cwID))
		resp, err := svc.GetToolResult(as("test-user"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: branch.chatID, ToolCallId: reused,
		}))
		require.NoError(t, err)
		assert.Equal(t, full, resp.Msg.Content)
	})

	t.Run("a branch of another user's chat never reads it", func(t *testing.T) {
		// Not a state BranchChat can produce (a branch is its source's
		// user's), so this pins the explicit ownership check, not the
		// context-window walk.
		branch := seedToolResult(t, repo, "test-user", "", "", ptr.Of(theirs.cwID))
		_, err := svc.GetToolResult(as("test-user"), connect.NewRequest(&reliantv1.GetToolResultRequest{
			ChatId: branch.chatID, ToolCallId: reused,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("ListMessages sends the same result as a preview", func(t *testing.T) {
		resp, err := svc.ListMessages(as("test-user"), connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: mine.chatID}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.Messages, 1)
		block := resp.Msg.Messages[0].ContentBlocks[0]
		assert.True(t, block.GetTruncated())
		assert.Equal(t, int64(len(full)), block.GetOriginalSize())
		assert.LessOrEqual(t, len(block.GetContent()), messageproto.ToolResultPreviewBytes)
	})
}
