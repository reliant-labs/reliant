package streaming_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"

	streaming "github.com/reliant-labs/reliant/internal/handlers/streaming"
)

// The streams' catch-up, replay and live-delivery contracts are pinned by the
// streaming_*_test.go files beside this one. These pin who may open a stream,
// through the generated harness's server, which installs the caller the way
// the API server's auth interceptor does.
func TestStreamUserUpdates_Generated(t *testing.T) {
	t.Parallel()

	open := func(t *testing.T, client reliantv1connect.StreamingServiceClient, req *pb.StreamUserUpdatesRequest) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := client.StreamUserUpdates(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		for stream.Receive() {
			// The first frame is the user catch-up; a refused stream sends none.
		}
		return stream.Err()
	}
	openChat := func(t *testing.T, client reliantv1connect.StreamingServiceClient, req *pb.StreamChatUpdatesRequest) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := client.StreamChatUpdates(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		for stream.Receive() {
			// A refused stream sends no frame.
		}
		return stream.Err()
	}

	t.Run("anonymous caller is unauthenticated", func(t *testing.T) {
		t.Parallel()
		_, client := streaming.NewTestStreamingServer(t,
			streaming.WithStreamingDeps(streaming.Deps{Repo: foreignChatRepo()}),
			streaming.WithoutPrincipal())
		err := open(t, client, &pb.StreamUserUpdatesRequest{})
		require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err))
	})

	t.Run("anonymous caller cannot open a chat stream", func(t *testing.T) {
		t.Parallel()
		_, client := streaming.NewTestStreamingServer(t,
			streaming.WithStreamingDeps(streaming.Deps{Repo: foreignChatRepo()}),
			streaming.WithoutPrincipal())
		err := openChat(t, client, &pb.StreamChatUpdatesRequest{ChatId: "chat-1"})
		require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err))
	})

	t.Run("another user's chat is not found", func(t *testing.T) {
		t.Parallel()
		_, client := streaming.NewTestStreamingServer(t,
			streaming.WithStreamingDeps(streaming.Deps{Repo: foreignChatRepo()}),
			streaming.WithPrincipal(testkit.WithUserID("test-user")))
		err := openChat(t, client, &pb.StreamChatUpdatesRequest{ChatId: "chat-of-someone-else"})
		require.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("a chat stream names its chat", func(t *testing.T) {
		t.Parallel()
		_, client := streaming.NewTestStreamingServer(t,
			streaming.WithStreamingDeps(streaming.Deps{Repo: foreignChatRepo()}),
			streaming.WithPrincipal(testkit.WithUserID("test-user")))
		err := openChat(t, client, &pb.StreamChatUpdatesRequest{})
		require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})
}

// foreignChatRepo has an empty user update log and every chat belongs to
// another user. Everything past the ownership check is unreachable from the
// rows that use it: any other read fails with the mock's "Func not set" error,
// never a silent pass.
func foreignChatRepo() *streaming.MockRepository {
	return &streaming.MockRepository{
		GetLatestUserUpdateSequenceFunc: func(context.Context, string) (int64, error) { return 0, nil },
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: id, UserID: "someone-else"}, nil
		},
	}
}
