// Copyright (c) 2025 Reliant Labs
package messageconv

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A message persisted with display_style HIDDEN is harness-written; the skill
// suggester relies on message.Message.Hidden to tell it from a user request.
func TestDbMessagesToMessages_HiddenFollowsDisplayStyle(t *testing.T) {
	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	info := reliantv1.DisplayStyle_DISPLAY_STYLE_INFO
	repo := &countingRepo{
		blocks: map[string][]*db.MessageContentBlock{
			"h": {textBlock("h", "brief", 0)}, "i": {textBlock("i", "note", 0)}, "n": {textBlock("n", "typed", 0)},
		},
		windows: map[string]*db.ContextWindow{"cw": {ID: "cw", Sequence: 1}},
	}
	mk := func(id string, style *reliantv1.DisplayStyle) *db.Message {
		return &db.Message{ID: id, ContextWindowID: "cw", Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, DisplayStyle: style}
	}
	msgs, err := DbMessagesToMessages(context.Background(), []*db.Message{mk("h", &hidden), mk("i", &info), mk("n", nil)}, repo)
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	assert.True(t, msgs[0].Hidden)
	assert.False(t, msgs[1].Hidden)
	assert.False(t, msgs[2].Hidden)
}
