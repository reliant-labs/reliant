package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/pkg/middleware"
)

// Over the wire, through the svcerr interceptor a real server installs: the
// chat rpcs' bare svcerr kinds reach a Connect client with the code and the
// exact text the web and the CLI read. The markers here are contracts:
//   - "chat has not started" (FailedPrecondition) routes a send to StartChat
//     (frontends/web/src/lib/chatSendRouting.ts);
//   - Aborted is the selection version conflict ChatInput / chatSelection.ts
//     reload on.
func TestChatService_ErrorsOverTheWire(t *testing.T) {
	dbCtx, fx, repo, _, service := selectionFixture(t, db.Pending())
	_, client := testkit.NewConnectClient(t, service.Register, reliantv1connect.NewChatServiceClient,
		testkit.ServerPrincipal(middleware.ContextWithClaims))
	ctx := context.Background()

	t.Run("a send to a chat that has not started names StartChat", func(t *testing.T) {
		_, err := client.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "first message of a branch"))
		require.Equal(t, connect.CodeFailedPrecondition, wireCode(t, err))
		require.Equal(t, "chat has not started; call StartChat", wireMessage(t, err))
		require.Equal(t, errreason.ChatNotStarted, wireReason(t, err), "the web routes the send on the reason (13-p9 §7.1 #3)")
	})

	t.Run("a chat that does not exist is the store's NotFound", func(t *testing.T) {
		_, err := client.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: uuid.NewString()}))
		require.Equal(t, connect.CodeNotFound, wireCode(t, err))
		require.Equal(t, "chat not found", wireMessage(t, err))
	})

	t.Run("an edit against a changed selection is Aborted", func(t *testing.T) {
		first := setSelection(t, dbCtx, repo, fx.chatID, map[string]any{"mode": "auto"})
		setSelection(t, dbCtx, repo, fx.chatID, map[string]any{"mode": "plan"})

		stale := first.Version
		_, err := client.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{
			ChatId:          fx.chatID,
			ProviderPolicy:  &reliantv1.ProviderPolicy{OnLimit: reliantv1.ProviderPolicy_ON_LIMIT_WAIT},
			ExpectedVersion: &stale,
		}))
		require.Equal(t, connect.CodeAborted, wireCode(t, err))
		require.Equal(t, "the chat's selection changed since it was read; reload and try again", wireMessage(t, err))
	})

	t.Run("a malformed request keeps its validator's words", func(t *testing.T) {
		_, err := client.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{ChatId: fx.chatID}))
		require.Equal(t, connect.CodeInvalidArgument, wireCode(t, err))
		require.Equal(t, "thread_id is required", wireMessage(t, err))
	})
}

// wireCode and wireMessage read what the client received. It is a
// *connect.Error, built by the server's interceptor; svcerr hands one back
// as it is.
func wireCode(t *testing.T, err error) connect.Code {
	t.Helper()
	require.IsType(t, &connect.Error{}, err)
	return svcerr.Code(err)
}

func wireReason(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	return ce.Meta().Get(svcerr.ReasonHeader)
}

func wireMessage(t *testing.T, err error) string {
	t.Helper()
	return svcerr.ToConnect(err).Message()
}
