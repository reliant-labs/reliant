package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// A redacted_thinking block's content is Anthropic's sealed ciphertext. It is
// stored so the next turn can replay it to the provider unchanged, and no
// client ever receives it: every chat read sends the block, with its type and
// position, and an empty content. The provider replay reads the same stored
// row and still gets the ciphertext whole.
func TestRedactedThinkingCiphertextNeverReachesAClient(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := NewTestChat(t, WithChatDeps(Deps{TriggerReads: &MockTriggerReads{}, Repo: repo, Chats: chataccess.NewFromORM(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}))
	f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")

	const ciphertext = "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpPkNRj2YfWXGmKDxH4mUcR0FnMPF3A=="
	const position = 6
	sealedID := addBlock(t, repo, f.AssistantMessageID, position, &db.MessageContentBlock{
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
		Content:   ptr.Of(ciphertext),
	})

	t.Run("ListMessages sends the block blank in every mode", func(t *testing.T) {
		for _, mode := range []*reliantv1.ToolBodyMode{nil, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum(), reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum()} {
			resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.ChatID, ToolBodies: mode}))
			require.NoError(t, err)
			block := blocksByID(resp.Msg.Messages)[sealedID]
			require.NotNil(t, block, "the block is still listed, mode %v", mode)
			assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING, block.GetType())
			assert.EqualValues(t, position, block.GetIndex())
			require.NotNil(t, block.Content, "content is present and empty, mode %v", mode)
			assert.Empty(t, block.GetContent(), "mode %v", mode)
			assert.Nil(t, block.BodyElided, "nothing to fetch, mode %v", mode)
			assert.Nil(t, block.BodySize, "mode %v", mode)
			wire, err := json.Marshal(resp.Msg)
			require.NoError(t, err)
			assert.NotContains(t, string(wire), ciphertext, "mode %v", mode)
		}
	})

	t.Run("GetContentBlockBodies sends it blank, even with full", func(t *testing.T) {
		for _, full := range []*bool{nil, ptr.Of(true)} {
			resp, err := svc.GetContentBlockBodies(ctx, connect.NewRequest(&reliantv1.GetContentBlockBodiesRequest{
				ChatId: f.ChatID, BlockIds: []string{sealedID}, Full: full,
			}))
			require.NoError(t, err)
			require.Len(t, resp.Msg.Bodies, 1)
			require.NotNil(t, resp.Msg.Bodies[0].Content)
			assert.Empty(t, resp.Msg.Bodies[0].GetContent())
		}
	})

	t.Run("GetChatUpdates sends it blank, from new and old update rows", func(t *testing.T) {
		// A minimal row, enriched from the blocks on read (the write-side
		// serializer), and a row written before blanking, carrying the
		// ciphertext in its own content_blocks.
		require.NoError(t, repo.CreateChatUpdate(ctx, f.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
			f.AssistantMessageID, `{"id":"`+f.AssistantMessageID+`"}`))
		old, err := json.Marshal(map[string]any{"id": f.AssistantMessageID, "content_blocks": []any{
			map[string]any{"id": sealedID, "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING), "index": position, "content": ciphertext},
		}})
		require.NoError(t, err)
		require.NoError(t, repo.CreateChatUpdate(ctx, f.ChatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
			f.AssistantMessageID, string(old)))

		resp, err := svc.GetChatUpdates(ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{ChatId: f.ChatID}))
		require.NoError(t, err)
		seen := 0
		for _, u := range resp.Msg.Updates {
			assert.NotContains(t, u.Data, ciphertext)
			if strings.Contains(u.Data, sealedID) {
				seen++
			}
		}
		assert.Equal(t, 2, seen, "both rows still carry the block")
	})

	t.Run("the provider replay reads the stored ciphertext whole", func(t *testing.T) {
		msg, err := repo.GetMessage(ctx, f.AssistantMessageID)
		require.NoError(t, err)
		history, err := messageconv.DbMessagesToMessages(ctx, []*db.Message{msg}, repo)
		require.NoError(t, err)
		require.Len(t, history, 1)
		var replayed []string
		for _, part := range history[0].Parts {
			if sealed, ok := part.(message.RedactedReasoningContent); ok {
				replayed = append(replayed, sealed.Data)
			}
		}
		assert.Equal(t, []string{ciphertext}, replayed)
	})
}
