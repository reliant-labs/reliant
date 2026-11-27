// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
)

// PrepareSend is Prepare in primitives: the same title and hash, and its
// refusals unwrapped, for the tool to wrap.
func TestPrepareSend(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	title, hash, err := f.approver.PrepareSend(context.Background(), agent, "item-1", 3, false)
	require.NoError(t, err)
	assert.Equal(t, card.Title, title)
	assert.Equal(t, card.Hash, hash)

	_, _, err = f.approver.PrepareSend(context.Background(), agent, "item-1", 3, true)
	assert.Same(t, email.ErrUnattended, err, "unwrapped")
	_, _, err = f.approver.PrepareSend(context.Background(), agent, "item-1", 2, false)
	var conflict *gtm.RevisionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, int32(3), conflict.Current)
}

// SendApproved is Execute in primitives: it sends under the approved card,
// and returns its refusals unwrapped.
func TestSendApproved(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	_, hash, err := f.approver.PrepareSend(context.Background(), agent, "item-1", 3, false)
	require.NoError(t, err)

	_, err = f.approver.SendApproved(context.Background(), agent, "item-1", 3, sendRun)
	assert.Same(t, email.ErrNoApproval, err, "unwrapped")
	assert.Zero(t, sender.count())

	sent, err := f.approver.SendApproved(approvedCtx(hash), agent, "item-1", 3, sendRun)
	require.NoError(t, err)
	assert.Equal(t, gtm.ItemSent, sent.Status)
	assert.Equal(t, 1, sender.count())

	_, err = f.approver.SendApproved(approvedCtx(hash), agent, "item-1", 3, sendRun)
	assert.Same(t, gtm.ErrAlreadySent, err, "unwrapped")
}
