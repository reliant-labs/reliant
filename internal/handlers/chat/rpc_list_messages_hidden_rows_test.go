package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// A scroll-back page is sized over the rows the transcript renders. HIDDEN
// rows (mailbox envelopes, sub-agent result bodies, run-to-run deliveries,
// compaction summaries) are dropped from every display response, so a page
// sized over them came back short — and a page window of nothing but hidden
// rows came back EMPTY with has_more true. Orchestrator chats write runs of
// hidden main-thread rows longer than a page; the web client read the short
// page as the start of the chat and showed "Start of conversation" in the
// middle of the transcript.
func TestListMessages_ScrollBackPagesCountOnlyTranscriptRows(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	// The chat id doubles as the main thread id (a chat with no workflow).
	chatID := uuid.New().String()
	cwID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "hidden rows", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: chatID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	// 3 hidden, v0..v3, 150 hidden (longer than a 100-row page), v4..v6, 2 hidden.
	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	var seq int64
	seqOf := map[string]int64{}
	write := func(id string, hide bool) {
		seq++
		msg := &db.Message{
			ID: id, ChatID: chatID, ThreadID: chatID, ContextWindowID: cwID,
			Ordinal: seq, Seq: seq, Role: reliantv1.MessageRole_MESSAGE_ROLE_USER,
			CreatedAt: now, UpdatedAt: now,
		}
		if hide {
			msg.DisplayStyle = &hidden
		}
		require.NoError(t, repo.CreateMessage(ctx, msg))
		seqOf[id] = seq
	}
	hiddenRun := func(n int) {
		for i := 0; i < n; i++ {
			write(uuid.New().String(), true)
		}
	}
	visible := func(from, to int) {
		for i := from; i <= to; i++ {
			write(fmt.Sprintf("v%d-%s", i, chatID), false)
		}
	}
	vid := func(i int) string { return fmt.Sprintf("v%d-%s", i, chatID) }
	hiddenRun(3)
	visible(0, 3)
	hiddenRun(150)
	visible(4, 6)
	hiddenRun(2)

	svc := &Service{deps: Deps{Repo: repo}}
	page := func(beforeSeq int64, recent int32) *reliantv1.ListMessagesResponse {
		t.Helper()
		req := &reliantv1.ListMessagesRequest{ChatId: chatID, Recent: &recent}
		if beforeSeq > 0 {
			req.BeforeSeq = &beforeSeq
		}
		resp, err := svc.ListMessages(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg
	}
	ids := func(resp *reliantv1.ListMessagesResponse) []string {
		out := make([]string, len(resp.Messages))
		for i, m := range resp.Messages {
			out[i] = m.Id
		}
		return out
	}

	t.Run("a page window of hidden rows below the cursor", func(t *testing.T) {
		// The client holds v4..v6 and asks for 100 more. The 100 rows below
		// v4 are all hidden: counted, they made an empty page with has_more.
		resp := page(seqOf[vid(4)], 100)
		require.Equal(t, []string{vid(0), vid(1), vid(2), vid(3)}, ids(resp))
		require.False(t, resp.HasMore, "only hidden rows lie below v0")
		require.Equal(t, seqOf[vid(0)], resp.OldestSeq)
	})

	t.Run("the newest page is full of transcript rows", func(t *testing.T) {
		resp := page(0, 100)
		require.Equal(t, []string{vid(0), vid(1), vid(2), vid(3), vid(4), vid(5), vid(6)}, ids(resp))
		require.False(t, resp.HasMore)
	})

	t.Run("scroll-back tiles the transcript with full pages", func(t *testing.T) {
		var walked []string
		var cursor int64
		for pages := 0; ; pages++ {
			require.Less(t, pages, 10, "scroll-back never terminated")
			resp := page(cursor, 2)
			require.NotEmpty(t, resp.Messages, "page below %d is empty", cursor)
			if resp.HasMore {
				require.Len(t, resp.Messages, 2, "every page but the last is full")
			}
			walked = append(ids(resp), walked...)
			if !resp.HasMore {
				break
			}
			cursor = resp.OldestSeq
		}
		require.Equal(t, []string{vid(0), vid(1), vid(2), vid(3), vid(4), vid(5), vid(6)}, walked)
	})
}
