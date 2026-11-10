package chat_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// GetChatUpdates under ledger retention (store.CompactChatUpdates): every
// response carries the compaction watermark (ChatCompactedThroughHeader), read
// after the updates.

// A poll from below the compaction watermark is told so: the updates it got
// are the compacted view, not every transition. Old pollers ignore the header
// and read what the compacted log holds — each entity's current state.
func TestGetChatUpdates_BelowTheCompactionWatermarkSaysSo(t *testing.T) {
	t.Parallel()
	repo := newChatRepo()
	repo.GetUpdatesSinceFunc = func(context.Context, string, int64, int) ([]db.ChatUpdate, error) {
		return []db.ChatUpdate{{ChatID: ownChatID, SequenceNumber: 40, EntityID: "wf:n1", Data: []byte(`{}`),
			UpdateType: pb.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION}}, nil
	}
	repo.GetLatestUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 90, nil }
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return 50, nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	resp, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 3}))
	require.NoError(t, err)
	require.Equal(t, "50", resp.Header().Get(chat.ChatCompactedThroughHeader))
	require.Len(t, resp.Msg.GetUpdates(), 1)
}

// Compaction racing the poll: the watermark is read after the updates, so a
// compaction that deleted under this read is reported by this response, not
// the next one.
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

	resp, err := svc.GetChatUpdates(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 10}))
	require.NoError(t, err)
	require.Equal(t, "60", resp.Header().Get(chat.ChatCompactedThroughHeader))
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
