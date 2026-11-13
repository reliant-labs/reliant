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
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func bodiesByID(resp *reliantv1.GetContentBlockBodiesResponse) map[string]*reliantv1.ContentBlockBody {
	byID := map[string]*reliantv1.ContentBlockBody{}
	for _, b := range resp.Bodies {
		byID[b.BlockId] = b
	}
	return byID
}

// addBlock appends a block to an existing message.
func addBlock(t *testing.T, repo *db.Repo, msgID string, pos int, b *db.MessageContentBlock) string {
	t.Helper()
	now := time.Now().UTC()
	b.ID, b.MessageID, b.Position = uuid.New().String(), msgID, pos
	b.Version, b.CreatedAt, b.UpdatedAt = ptr.Of(1), now, now
	require.NoError(t, repo.CreateContentBlock(context.Background(), b))
	return b.ID
}

func TestGetContentBlockBodies(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	as := func(userID string) context.Context {
		return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	}
	ctx := as("test-user")
	svc := &Service{deps: Deps{Repo: repo}}
	mine := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
	theirs := dbtest.SeedTranscriptBodies(t, as("someone-else"), repo, "someone-else")

	get := func(t *testing.T, ctx context.Context, req *reliantv1.GetContentBlockBodiesRequest) *reliantv1.GetContentBlockBodiesResponse {
		t.Helper()
		resp, err := svc.GetContentBlockBodies(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg
	}

	t.Run("the chat's own blocks: inputs whole, results at the display preview, thinking whole", func(t *testing.T) {
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{
			ChatId:   mine.ChatID,
			BlockIds: []string{mine.DoneCallBlockID, mine.ResultBlockID, mine.ThinkingBlockID, mine.DoneCallBlockID},
		})
		assert.Empty(t, resp.NotFound)
		assert.Empty(t, resp.Deferred)
		require.Len(t, resp.Bodies, 3, "a repeated id is answered once")
		assert.Equal(t, []string{mine.DoneCallBlockID, mine.ResultBlockID, mine.ThinkingBlockID},
			[]string{resp.Bodies[0].BlockId, resp.Bodies[1].BlockId, resp.Bodies[2].BlockId}, "request order")
		bodies := bodiesByID(resp)

		assert.Equal(t, mine.DoneInput, bodies[mine.DoneCallBlockID].GetInput())
		assert.Equal(t, mine.Thinking, bodies[mine.ThinkingBlockID].GetContent())
		result := bodies[mine.ResultBlockID]
		assert.True(t, result.GetTruncated())
		assert.Equal(t, int64(len(mine.Result)), result.GetOriginalSize())
		assert.LessOrEqual(t, len(result.GetContent()), messageproto.ToolResultPreviewBytes)
		want, _ := messageproto.PreviewToolResult("edit", mine.Result)
		assert.Equal(t, want, result.GetContent(), "what an expanded row shows, as FULL sends it")
	})

	t.Run("full returns the result as stored", func(t *testing.T) {
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{
			ChatId: mine.ChatID, BlockIds: []string{mine.ResultBlockID}, Full: ptr.Of(true),
		})
		require.Len(t, resp.Bodies, 1)
		assert.Equal(t, mine.Result, resp.Bodies[0].GetContent())
		assert.Nil(t, resp.Bodies[0].Truncated)
	})

	t.Run("another user's block is not found, even named under the caller's own chat", func(t *testing.T) {
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{
			ChatId:   mine.ChatID,
			BlockIds: []string{theirs.DoneCallBlockID, theirs.ResultBlockID, "no-such-block", mine.SmallCallBlockID},
		})
		assert.Equal(t, []string{theirs.DoneCallBlockID, theirs.ResultBlockID, "no-such-block"}, resp.NotFound)
		require.Len(t, resp.Bodies, 1)
		assert.Equal(t, mine.SmallInput, resp.Bodies[0].GetInput())
	})

	t.Run("another user's chat is NOT_FOUND", func(t *testing.T) {
		_, err := svc.GetContentBlockBodies(ctx, connect.NewRequest(&reliantv1.GetContentBlockBodiesRequest{
			ChatId: theirs.ChatID, BlockIds: []string{theirs.DoneCallBlockID},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("a block of another of the caller's chats, not inherited, is not found", func(t *testing.T) {
		other := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: mine.ChatID, BlockIds: []string{other.DoneCallBlockID}})
		assert.Equal(t, []string{other.DoneCallBlockID}, resp.NotFound)
		assert.Empty(t, resp.Bodies)
	})

	t.Run("a branch reads the blocks it inherited", func(t *testing.T) {
		branch := seedToolResult(t, repo, "test-user", "", "", ptr.Of(mine.ContextWindowID))
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{
			ChatId: branch.chatID, BlockIds: []string{mine.DoneCallBlockID, mine.ResultBlockID},
		})
		assert.Empty(t, resp.NotFound)
		assert.Equal(t, mine.DoneInput, bodiesByID(resp)[mine.DoneCallBlockID].GetInput())
	})

	t.Run("a branch of another user's chat never reads it", func(t *testing.T) {
		branch := seedToolResult(t, repo, "test-user", "", "", ptr.Of(theirs.ContextWindowID))
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: branch.chatID, BlockIds: []string{theirs.DoneCallBlockID}})
		assert.Equal(t, []string{theirs.DoneCallBlockID}, resp.NotFound)
	})

	t.Run("a block of a hidden message is not found", func(t *testing.T) {
		f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
		now := time.Now().UTC()
		hiddenID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: hiddenID, ChatID: f.ChatID, Ordinal: 3, Seq: 3, ThreadID: f.ThreadID, ContextWindowID: f.ContextWindowID,
			Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, DisplayStyle: ptr.Of(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN),
			CreatedAt: now, UpdatedAt: now,
		}))
		blockID := addBlock(t, repo, hiddenID, 0, &db.MessageContentBlock{
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("a compaction summary"),
		})
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: f.ChatID, BlockIds: []string{blockID}})
		assert.Equal(t, []string{blockID}, resp.NotFound)
	})

	t.Run("an empty id is not found", func(t *testing.T) {
		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: mine.ChatID, BlockIds: []string{"", mine.SmallCallBlockID}})
		assert.Equal(t, []string{""}, resp.NotFound)
		assert.Len(t, resp.Bodies, 1)
	})

	t.Run("more than 100 ids is INVALID_ARGUMENT", func(t *testing.T) {
		ids := make([]string, maxContentBlockBodyIDs+1)
		for i := range ids {
			ids[i] = uuid.New().String()
		}
		_, err := svc.GetContentBlockBodies(ctx, connect.NewRequest(&reliantv1.GetContentBlockBodiesRequest{ChatId: mine.ChatID, BlockIds: ids}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: mine.ChatID, BlockIds: ids[:maxContentBlockBodyIDs]})
		assert.Len(t, resp.NotFound, maxContentBlockBodyIDs, "100 is allowed")
	})

	t.Run("bodies past the byte budget are deferred, and one is always returned", func(t *testing.T) {
		f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
		input := func(n int) *string { return ptr.Of(`{"content":"` + strings.Repeat("x", n) + `"}`) }
		call := func(pos, n int) string {
			return addBlock(t, repo, f.AssistantMessageID, pos, &db.MessageContentBlock{
				BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
				ToolCallID: ptr.Of("toolu_" + uuid.New().String()), ToolName: ptr.Of("write"), ToolInput: input(n),
			})
		}
		a, b, c := call(10, 3<<19), call(11, 3<<19), call(12, 3<<19) // 1.5 MiB each
		huge := call(13, 5<<20)

		resp := get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: f.ChatID, BlockIds: []string{a, b, c, f.SmallCallBlockID}})
		require.Len(t, resp.Bodies, 2)
		assert.Equal(t, a, resp.Bodies[0].BlockId)
		assert.Equal(t, b, resp.Bodies[1].BlockId)
		assert.Equal(t, []string{c, f.SmallCallBlockID}, resp.Deferred, "deferred in order, so the client's next ask resumes where this stopped")

		resp = get(t, ctx, &reliantv1.GetContentBlockBodiesRequest{ChatId: f.ChatID, BlockIds: []string{huge, a}})
		require.Len(t, resp.Bodies, 1, "a body over the budget alone is still returned")
		assert.Equal(t, huge, resp.Bodies[0].BlockId)
		assert.Equal(t, []string{a}, resp.Deferred)
	})
}

// GetToolResult still answers, on the visibility rule it now shares with
// GetContentBlockBodies (TestGetToolResult covers its cases); this pins that
// both agree for a branch.
func TestGetToolResultAndBodiesAgreeOnBranches(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo}}
	mine := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")
	branch := seedToolResult(t, repo, "test-user", "", "", ptr.Of(mine.ContextWindowID))

	full, err := svc.GetToolResult(ctx, connect.NewRequest(&reliantv1.GetToolResultRequest{ChatId: branch.chatID, ToolCallId: mine.DoneCallID}))
	require.NoError(t, err)
	bodies, err := svc.GetContentBlockBodies(ctx, connect.NewRequest(&reliantv1.GetContentBlockBodiesRequest{
		ChatId: branch.chatID, BlockIds: []string{mine.ResultBlockID}, Full: ptr.Of(true),
	}))
	require.NoError(t, err)
	require.Len(t, bodies.Msg.Bodies, 1)
	assert.Equal(t, full.Msg.Content, bodies.Msg.Bodies[0].GetContent())
}
