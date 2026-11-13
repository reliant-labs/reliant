// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A chat asking before it moves to the person's own AI account is a blocking
// item: its run is parked until the person answers.
func TestInboxPersonalFundingQuestionBlocks(t *testing.T) {
	t.Parallel()
	item := inboxItemFromPending(&core.InboxPending{
		Kind: core.InboxPendingPersonalFunding, ItemKey: "c1@20261113T060000.000000", ChatID: "c1",
		WaitingSince: time.Now(), Text1: core.PersonalFundingLimited, Text2: "claude-opus",
	})
	assert.Equal(t, reliantv1.InboxItemKind_INBOX_ITEM_KIND_PERSONAL_FUNDING, item.GetKind())
	assert.Equal(t, inboxPersonalFundingPrefix+"c1@20261113T060000.000000", item.GetItemId())
	assert.True(t, inboxItemBlocks(item))
}
