package chat_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestDeleteChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteChatRequest, pb.DeleteChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		// The repo's DeleteChat is not programmed here, so reaching it would
		// fail the case: an active chat is refused before anything is deleted
		// or archived.
		{
			Name:    "a chat that is not archived is refused, not archived",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{ChatId: ownChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.DeleteChat)
}

// DeleteChat means one thing: an archived chat is deleted for good. It used
// to archive an active chat instead, so the same request had opposite
// effects depending on state the caller might not have seen.
func TestDeleteChat_DeletesAnArchivedChatPermanently(t *testing.T) {
	t.Parallel()

	repo := newChatRepo()
	var deleted []string
	repo.DeleteChatFunc = func(_ context.Context, id string) error {
		deleted = append(deleted, id)
		return nil
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	resp, err := svc.DeleteChat(chat.AuthedContext(t), connect.NewRequest(&pb.DeleteChatRequest{ChatId: archivedChatID}))
	require.NoError(t, err)
	require.True(t, resp.Msg.GetPermanentlyDeleted())
	require.Equal(t, []string{archivedChatID}, deleted)
}
