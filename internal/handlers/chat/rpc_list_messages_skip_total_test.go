// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// messageCountSpy is the real repository with every message COUNT a
// ListMessages total can issue counted on its way through.
type messageCountSpy struct {
	*db.Repo
	counts atomic.Int32
}

func (r *messageCountSpy) CountMessagesByContextWindow(ctx context.Context, contextWindowID string) (int, error) {
	r.counts.Add(1)
	return r.Repo.CountMessagesByContextWindow(ctx, contextWindowID)
}

func (r *messageCountSpy) CountMessagesByContextWindowUpToSeq(ctx context.Context, contextWindowID string, maxSeq int64) (int, error) {
	r.counts.Add(1)
	return r.Repo.CountMessagesByContextWindowUpToSeq(ctx, contextWindowID, maxSeq)
}

func (r *messageCountSpy) CountMessagesInChatOutsideThread(ctx context.Context, chatID, threadID string) (int, error) {
	r.counts.Add(1)
	return r.Repo.CountMessagesInChatOutsideThread(ctx, chatID, threadID)
}

func (r *messageCountSpy) CountMessagesInThread(ctx context.Context, threadID string) (int, error) {
	r.counts.Add(1)
	return r.Repo.CountMessagesInThread(ctx, threadID)
}

// TestListMessages_SkipTotal proves a skip_total read (what every first-party
// caller sends) runs no count on any path and leaves total 0, and that a
// request without it (an older client) still gets the exact total. The count
// walked every message of the chat: on one of a million it took 9.9s and timed
// the read out (obs 16122d5e).
func TestListMessages_SkipTotal(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	const totalMessages = 25
	ctx, chatID := createChatWithMessages(t, repo, totalMessages)
	spy := &messageCountSpy{Repo: repo}
	svc := &Service{deps: Deps{Repo: spy}}

	recent := int32(10)
	beforeSeq := int64(20)
	threadID := chatID
	reads := map[string]*reliantv1.ListMessagesRequest{
		"newest page":      {ChatId: chatID, Recent: &recent},
		"scroll-back page": {ChatId: chatID, Recent: &recent, BeforeSeq: &beforeSeq},
		"one thread":       {ChatId: chatID, ThreadId: &threadID},
		"whole chat":       {ChatId: chatID},
	}

	for name, req := range reads {
		t.Run(name, func(t *testing.T) {
			skip := proto.Clone(req).(*reliantv1.ListMessagesRequest)
			skip.SkipTotal = true
			before := spy.counts.Load()
			resp, err := svc.ListMessages(ctx, connect.NewRequest(skip))
			require.NoError(t, err)
			require.NotEmpty(t, resp.Msg.Messages, "the page itself is still read")
			require.Zero(t, resp.Msg.Total, "skip_total leaves total unset")
			require.Equal(t, before, spy.counts.Load(), "skip_total must run no count query")
		})
	}

	t.Run("old client still gets the exact total", func(t *testing.T) {
		before := spy.counts.Load()
		resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
			ChatId: chatID, Recent: &recent,
		}))
		require.NoError(t, err)
		require.EqualValues(t, totalMessages, resp.Msg.Total)
		require.Greater(t, spy.counts.Load(), before, "the total comes from the count")
	})
}
