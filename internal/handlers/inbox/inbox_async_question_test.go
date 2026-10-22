// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// An async question (ask_user without blocking: true) is a question item marked async,
// and it blocks nothing: the agent kept working, so it is not counted in the
// badge's blocking number. A blocking question still is.
func TestInboxAsyncQuestionIsNotBlocking(t *testing.T) {
	t.Parallel()
	pending := func(async int32) *core.InboxPending {
		return &core.InboxPending{
			Kind: core.InboxPendingQuestion, ItemKey: "q1", ChatID: "c1", WaitingSince: time.Now(),
			Text1: "thread-1", Text2: `{"questions":[{"question":"Which database?"}]}`, Int1: async,
		}
	}

	async := inboxItemFromPending(pending(1))
	assert.True(t, async.GetQuestion().GetAsync())
	assert.Equal(t, "Which database?", async.GetQuestion().GetPrompt())
	assert.False(t, inboxItemBlocks(async), "an async question blocks no run")

	blocking := inboxItemFromPending(pending(0))
	assert.False(t, blocking.GetQuestion().GetAsync())
	assert.True(t, inboxItemBlocks(blocking))
}
