// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// authorHistoryStore serves each row's one text block.
func authorHistoryStore(rows []*db.Message, texts []string) *MockMessageRepairStore {
	return &MockMessageRepairStore{
		ListContentBlocksForMessagesFunc: func(_ context.Context, ids []string) ([]*db.MessageContentBlock, error) {
			var out []*db.MessageContentBlock
			for i, r := range rows {
				text := texts[i]
				out = append(out, &db.MessageContentBlock{ID: r.ID + "-b", MessageID: r.ID, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: &text})
			}
			return out, nil
		},
		GetContextWindowFunc: func(_ context.Context, id string) (*db.ContextWindow, error) {
			return &db.ContextWindow{ID: id}, nil
		},
	}
}

func authorRow(id string, role reliantv1.MessageRole) *db.Message {
	return &db.Message{ID: id, ChatID: "chat", ThreadID: "main", ContextWindowID: "cw", Role: role}
}

func strp(s string) *string { return &s }

// The prompt-assembly boundary renders authors: a single-person history is
// exactly what message conversion produced, and once a row records another
// author, the human turns from it on are tagged while every agent, hidden and
// model row is left alone.
func TestConvertAndRepairMessages_AuthorTags(t *testing.T) {
	ctx := context.Background()
	user := reliantv1.MessageRole_MESSAGE_ROLE_USER
	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	agentKind := reliantv1.AgentMessageKind(2)

	ownerTurn := authorRow("m1", user)
	reply := authorRow("m2", reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	collab := authorRow("m3", user)
	collab.AuthorUserID, collab.AuthorName = strp("u-dana"), strp("Dana")
	envelope := authorRow("m4", user)
	envelope.DisplayStyle = &hidden
	fromAgent := authorRow("m5", user)
	fromAgent.SenderThreadID, fromAgent.SenderKind = strp("t-sub"), &agentKind
	ownerAgain := authorRow("m6", user)

	t.Run("single author: unchanged", func(t *testing.T) {
		rows := []*db.Message{ownerTurn, reply, ownerAgain}
		texts := []string{"hi", "hello", "more"}
		store := authorHistoryStore(rows, texts)
		want, err := messageconv.DbMessagesToMessages(ctx, rows, store)
		require.NoError(t, err)
		got, err := ConvertAndRepairMessages(ctx, rows, store, nil)
		require.NoError(t, err)
		require.True(t, reflect.DeepEqual(want, got), "a single-author history must reach the model exactly as converted:\n%#v\n%#v", want, got)
	})

	t.Run("two authors: tagged from the first authored row", func(t *testing.T) {
		rows := []*db.Message{ownerTurn, reply, collab, envelope, fromAgent, ownerAgain}
		texts := []string{"hi", "hello", "look", "<system>queued</system>", "from a sub-agent", "thanks"}
		got, err := ConvertAndRepairMessages(ctx, rows, authorHistoryStore(rows, texts), nil)
		require.NoError(t, err)
		var rendered []string
		for _, m := range got {
			var s string
			for i, p := range m.Parts {
				if i > 0 {
					s += "|"
				}
				s += p.(message.TextContent).Text
			}
			rendered = append(rendered, s)
		}
		require.Equal(t, []string{
			"hi", "hello",
			`<message from="Dana" earlier_turns_from="the owner">|look`,
			"<system>queued</system>",
			"from a sub-agent",
			`<message from="the owner">|thanks`,
		}, rendered)
	})

	// The owner's rows carry their name once the send path stamps it
	// (author_name, author_user_id NULL). The name the owner's turns render
	// under comes from rows up to the first collaborator's turn only, so a
	// later rename never rewrites a turn already sent.
	render := func(t *testing.T, rows []*db.Message, texts []string) []string {
		t.Helper()
		got, err := ConvertAndRepairMessages(ctx, rows, authorHistoryStore(rows, texts), nil)
		require.NoError(t, err)
		var out []string
		for _, m := range got {
			var s string
			for i, p := range m.Parts {
				if i > 0 {
					s += "|"
				}
				s += p.(message.TextContent).Text
			}
			out = append(out, s)
		}
		return out
	}
	t.Run("the owner's stamped name names their turns", func(t *testing.T) {
		named := authorRow("m1", user)
		named.AuthorName = strp("Alex")
		renamed := authorRow("m6", user)
		renamed.AuthorName = strp("Alexandra")
		rows := []*db.Message{named, reply, collab, renamed}
		require.Equal(t, []string{
			"hi", "hello",
			`<message from="Dana" earlier_turns_from="Alex">|look`,
			`<message from="Alex">|thanks`,
		}, render(t, rows, []string{"hi", "hello", "look", "thanks"}), "a rename after the first collaborator turn changes nothing already rendered")
	})
	t.Run("an owner named only after the first collaborator turn stays the owner", func(t *testing.T) {
		laterNamed := authorRow("m6", user)
		laterNamed.AuthorName = strp("Alex")
		rows := []*db.Message{ownerTurn, reply, collab, laterNamed}
		require.Equal(t, []string{
			"hi", "hello",
			`<message from="Dana" earlier_turns_from="the owner">|look`,
			`<message from="the owner">|thanks`,
		}, render(t, rows, []string{"hi", "hello", "look", "thanks"}), "bytes sent before the owner had a name never change")
	})
	t.Run("an owner's stamped name alone changes nothing", func(t *testing.T) {
		named := authorRow("m1", user)
		named.AuthorName = strp("Alex")
		rows := []*db.Message{named, reply}
		store := authorHistoryStore(rows, []string{"hi", "hello"})
		want, err := messageconv.DbMessagesToMessages(ctx, rows, store)
		require.NoError(t, err)
		got, err := ConvertAndRepairMessages(ctx, rows, store, nil)
		require.NoError(t, err)
		require.True(t, reflect.DeepEqual(want, got), "a single-person chat whose owner rows are stamped still sends byte-identical requests")
	})
}
