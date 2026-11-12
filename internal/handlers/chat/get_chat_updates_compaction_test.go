package chat_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// GetChatUpdates under ledger retention (store.CompactChatUpdates): every
// response carries the compaction watermark (ChatCompactedThroughHeader), read
// after the updates, and a poll from below it is refused.

// A poll from below the compaction watermark is refused, not answered with a
// partial replay: a poller that does not know about compaction (every client
// released before it) would otherwise advance its cursor past MESSAGE and
// TOOL_CALL updates and node transitions it never saw. The error names the
// watermark, so a poller that knows re-reads the chat and resumes there.
func TestGetChatUpdates_BelowTheCompactionWatermarkIsRefused(t *testing.T) {
	t.Parallel()
	repo := newChatRepo()
	repo.GetUpdatesSinceFunc = func(context.Context, string, int64, int) ([]db.ChatUpdate, error) {
		return []db.ChatUpdate{{ChatID: ownChatID, SequenceNumber: 40, EntityID: "wf:n1", Data: []byte(`{}`),
			UpdateType: pb.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION}}, nil
	}
	repo.GetLatestUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 90, nil }
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return 50, nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	for _, since := range []int64{0, 3, 49} {
		resp, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: since}))
		require.Error(t, err, "since_seq %d is below the watermark", since)
		require.Nil(t, resp)
		require.True(t, svcerr.IsOutOfRange(err), "since_seq %d: %v", since, err)
		cerr := svcerr.ToConnect(err)
		require.Equal(t, connect.CodeOutOfRange, cerr.Code())
		require.Equal(t, "50", cerr.Meta().Get(chat.ChatCompactedThroughHeader))
		require.Equal(t, chat.ChatHistoryCompactedReason, cerr.Meta().Get(svcerr.ReasonHeader))
	}
}

// At or above the watermark the log is whole: the poll succeeds and reports
// the watermark.
func TestGetChatUpdates_AtTheCompactionWatermarkReplays(t *testing.T) {
	t.Parallel()
	repo := newChatRepo()
	repo.GetUpdatesSinceFunc = func(context.Context, string, int64, int) ([]db.ChatUpdate, error) {
		return []db.ChatUpdate{{ChatID: ownChatID, SequenceNumber: 51, EntityID: "wf:n1", Data: []byte(`{}`),
			UpdateType: pb.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION}}, nil
	}
	repo.GetLatestUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 90, nil }
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return 50, nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	resp, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 50}))
	require.NoError(t, err)
	require.Equal(t, "50", resp.Header().Get(chat.ChatCompactedThroughHeader))
	require.Len(t, resp.Msg.GetUpdates(), 1)
}

// Compaction racing the poll: the watermark is read after the updates, so a
// compaction that deleted under this read refuses this response, rather than
// letting a partial read through and reporting it on the next one.
func TestGetChatUpdates_WatermarkIsReadAfterTheUpdates(t *testing.T) {
	t.Parallel()
	repo := newChatRepo()
	var compacted atomic.Int64
	repo.GetUpdatesSinceFunc = func(context.Context, string, int64, int) ([]db.ChatUpdate, error) {
		compacted.Store(60) // compaction runs while the updates are read
		return nil, nil
	}
	repo.GetLatestUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 90, nil }
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return compacted.Load(), nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	_, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 10}))
	require.True(t, svcerr.IsOutOfRange(err), "%v", err)
	require.Equal(t, "60", svcerr.ToConnect(err).Meta().Get(chat.ChatCompactedThroughHeader))
}

// An unreadable watermark fails the poll: the poller keeps its cursor and
// retries, instead of advancing past a log nobody vouched for.
func TestGetChatUpdates_UnreadableWatermarkFailsThePoll(t *testing.T) {
	t.Parallel()
	repo := newChatRepo()
	repo.GetUpdatesSinceFunc = func(context.Context, string, int64, int) ([]db.ChatUpdate, error) { return nil, nil }
	repo.GetLatestUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 90, nil }
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return 0, errors.New("db down") }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	_, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 10}))
	require.Error(t, err)
}
