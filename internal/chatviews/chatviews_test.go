package chatviews

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

type reads struct{}

func (reads) GetChat(context.Context, string) (*core.Chat, error) {
	return &core.Chat{ID: "own"}, nil
}

// A store that reads chats itself is its own reader, so a fixture that
// overrides the read is honoured.
func TestOf_AStoreThatReadsChatsIsItsOwnReader(t *testing.T) {
	t.Parallel()
	c, err := Of(reads{}).GetChat(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "own", c.ID)
}

type ambientReader struct{}

func (ambientReader) ResolveThreadAmbientMemoryExclusion(_ context.Context, chatID, threadID string, requested bool) (bool, error) {
	return chatID == "chat" && threadID == "thread" && requested, nil
}

func TestAmbientMemoryUsesTheProvidedReaderAndFailsClosedWithoutOne(t *testing.T) {
	t.Parallel()
	excluded, err := AmbientMemory(ambientReader{})(context.Background(), "chat", "thread", true)
	require.NoError(t, err)
	require.True(t, excluded)
	_, err = AmbientMemory(struct{}{})(context.Background(), "chat", "thread", false)
	require.ErrorIs(t, err, ErrNoChatReads)
}

// A store with no chat reads and no database handle fails loudly instead of
// returning a nil chat.
func TestOf_AStoreWithNoChatReadsFails(t *testing.T) {
	t.Parallel()
	for _, s := range []any{nil, struct{}{}} {
		c, err := Of(s).GetChat(context.Background(), "x")
		require.ErrorIs(t, err, ErrNoChatReads)
		require.Nil(t, c)
	}
}
