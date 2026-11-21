package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

type testPollInbox func(context.Context, *core.Trigger, string, string, PollItem, InboundEvent, time.Time) error

func (f testPollInbox) RecordPolledDelivery(ctx context.Context, t *core.Trigger, provider, conn string, item PollItem, event InboundEvent, at time.Time) error {
	return f(ctx, t, provider, conn, item, event, at)
}

func TestPollMovesCursorOnlyAfterInboxReceipt(t *testing.T) {
	repo, trigger, poller, starter, p := newPollEnv(t)
	ctx := t.Context()
	in := PollInput{TriggerID: trigger.ID}
	_, err := p.Poll(ctx, in)
	require.NoError(t, err)
	poller.add(PollItem{ID: "one", Type: "item.created"})
	receipts := 0
	persistErr := errors.New("no receipt written")
	p.WithDeliveryInbox(testPollInbox(func(_ context.Context, _ *core.Trigger, provider, conn string, item PollItem, ev InboundEvent, _ time.Time) error {
		receipts++
		assert.Equal(t, "feed", provider)
		assert.Equal(t, "conn-1", conn)
		assert.Equal(t, "one", item.ID)
		assert.Equal(t, trigger.ID+":one", ev.DedupeKey)
		reg, err := repo.GetTriggerRegistration(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, "0", reg.Cursor, "the cursor is unchanged when persistence is called")
		return persistErr
	}))
	_, err = p.Poll(ctx, in)
	require.ErrorIs(t, err, persistErr)
	reg, err := repo.GetTriggerRegistration(ctx, trigger.ID)
	require.NoError(t, err)
	require.Equal(t, "0", reg.Cursor)
	persistErr = nil
	out, err := p.Poll(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Accepted)
	reg, err = repo.GetTriggerRegistration(ctx, trigger.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", reg.Cursor)
	assert.Equal(t, 2, receipts)
	assert.Empty(t, starter.snapshot(), "the poll activity does not fan out inline")
}
