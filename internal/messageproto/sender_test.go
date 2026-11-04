package messageproto

import (
	"context"
	"errors"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReader struct {
	threads   []*db.Thread
	err       error
	listCalls int
	askedIDs  []string
}

func (f *fakeReader) ListToolCallsByMessageIDs(context.Context, []string) ([]*db.ToolCall, error) {
	return nil, nil
}

func (f *fakeReader) ListToolCallsByIDs(context.Context, []string) ([]*db.ToolCall, error) {
	return nil, nil
}

func (f *fakeReader) ListThreadsByIDs(_ context.Context, ids []string) ([]*db.Thread, error) {
	f.listCalls++
	f.askedIDs = ids
	return f.threads, f.err
}

func textBlock(messageID, text string) *db.MessageContentBlock {
	return &db.MessageContentBlock{
		ID: messageID + "-b", MessageID: messageID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of(text),
	}
}

func userMessage(id string, senderThread *string, senderKind *core.AgentMessageKind) *db.Message {
	return &db.Message{
		ID: id, ChatID: "chat", ThreadID: "main", Seq: 1,
		Role:           reliantv1.MessageRole_MESSAGE_ROLE_USER,
		SenderThreadID: senderThread,
		SenderKind:     senderKind,
		CreatedAt:      time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}
}

func TestFromDB_MapsSender(t *testing.T) {
	t.Parallel()

	kind := core.AgentMessageKindMessage
	msg := userMessage("m1", ptr.Of("peer"), &kind)
	msg.SenderSubject = ptr.Of("Tests pass; PR ready")

	got := FromDB(msg, nil, nil, &Options{SenderTitlesByThreadID: map[string]string{"peer": "Reviewer"}})

	require.NotNil(t, got.Sender)
	assert.Equal(t, "peer", got.Sender.ThreadId)
	assert.Equal(t, "Reviewer", got.Sender.Title)
	assert.Equal(t, reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE, got.Sender.Kind)
	assert.Equal(t, "Tests pass; PR ready", got.Sender.Subject)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, got.Role, "the role stays USER for the model")
}

func TestFromDB_NoSenderForTheHuman(t *testing.T) {
	t.Parallel()

	got := FromDB(userMessage("m1", nil, nil), nil, nil, nil)

	assert.Nil(t, got.Sender)
}

func TestFromDB_SenderWithoutTitleStillAttributed(t *testing.T) {
	t.Parallel()

	kind := core.AgentMessageKindMessage
	got := FromDB(userMessage("m1", ptr.Of("gone"), &kind), nil, nil, nil)

	require.NotNil(t, got.Sender)
	assert.Equal(t, "gone", got.Sender.ThreadId)
	assert.Empty(t, got.Sender.Title)
}

func TestAssembleForDisplay_BatchesSenderTitles(t *testing.T) {
	t.Parallel()

	kind := core.AgentMessageKindMessage
	messages := []*db.Message{
		userMessage("m1", ptr.Of("peer-a"), &kind),
		userMessage("m2", ptr.Of("peer-a"), &kind),
		userMessage("m3", ptr.Of("peer-b"), &kind),
		userMessage("m4", nil, nil),
	}
	blocks := map[string][]*db.MessageContentBlock{}
	for _, m := range messages {
		blocks[m.ID] = []*db.MessageContentBlock{textBlock(m.ID, "hi")}
	}
	reader := &fakeReader{threads: []*db.Thread{
		{ID: "peer-a", Title: ptr.Of("Alpha")},
		{ID: "peer-b"},
	}}

	got := AssembleForDisplay(context.Background(), reader, messages, blocks, nil, "main", 0)

	require.Len(t, got, 4)
	assert.Equal(t, 1, reader.listCalls, "sender titles are loaded once per response, not per message")
	assert.ElementsMatch(t, []string{"peer-a", "peer-b"}, reader.askedIDs, "each sender is asked for once")
	assert.Equal(t, "Alpha", got[0].Sender.Title)
	assert.Equal(t, "Alpha", got[1].Sender.Title)
	assert.Empty(t, got[2].Sender.Title)
	assert.Equal(t, "peer-b", got[2].Sender.ThreadId)
	assert.Nil(t, got[3].Sender)
}

func TestAssembleForDisplay_NoSendersNoThreadQuery(t *testing.T) {
	t.Parallel()

	messages := []*db.Message{userMessage("m1", nil, nil)}
	blocks := map[string][]*db.MessageContentBlock{"m1": {textBlock("m1", "hi")}}
	reader := &fakeReader{}

	AssembleForDisplay(context.Background(), reader, messages, blocks, nil, "main", 0)

	assert.Zero(t, reader.listCalls)
}

func TestAssembleForDisplay_TitleLookupFailureKeepsAttribution(t *testing.T) {
	t.Parallel()

	kind := core.AgentMessageKindMessage
	messages := []*db.Message{userMessage("m1", ptr.Of("peer"), &kind)}
	blocks := map[string][]*db.MessageContentBlock{"m1": {textBlock("m1", "hi")}}
	reader := &fakeReader{err: errors.New("db down")}

	got := AssembleForDisplay(context.Background(), reader, messages, blocks, nil, "main", 0)

	require.Len(t, got, 1)
	require.NotNil(t, got[0].Sender, "a failed title lookup must not turn an agent's message into the human's")
	assert.Equal(t, "peer", got[0].Sender.ThreadId)
	assert.Empty(t, got[0].Sender.Title)
}
