package streaming_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"

	streaming "github.com/reliant-labs/reliant/internal/handlers/streaming"
)

// failingStreamRepo fails the user log read with logErr and the chat read
// with chatErr. Everything else is unreachable: the embedded nil
// db.Repository panics if one is called.
type failingStreamRepo struct {
	db.Repository
	logErr, chatErr error
}

func (r failingStreamRepo) GetLatestUserUpdateSequence(context.Context, string) (int64, error) {
	return 0, r.logErr
}

func (r failingStreamRepo) GetChat(context.Context, string) (*db.Chat, error) {
	return nil, r.chatErr
}

// Over the wire, a stream refused by a repository failure reports a
// classified error's own code and redacts an opaque one. A missing chat stays
// NotFound, which the web's stream reader takes as permanent; an outage is
// now Internal, which it retries, instead of the NotFound it used to be.
func TestStreamUserUpdates_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	chatID := "chat-1"
	for _, tc := range []struct {
		name string
		repo failingStreamRepo
		req  *pb.StreamUserUpdatesRequest
		want connect.Code
	}{
		{"the user log times out", failingStreamRepo{logErr: context.DeadlineExceeded}, &pb.StreamUserUpdatesRequest{}, connect.CodeDeadlineExceeded},
		{"the user log fails opaquely", failingStreamRepo{logErr: errors.New("dsn=postgres://u:secret@h")}, &pb.StreamUserUpdatesRequest{}, connect.CodeInternal},
		{"the chat is missing", failingStreamRepo{chatErr: svcerr.NotFound("chat")}, &pb.StreamUserUpdatesRequest{SubscribeChatId: &chatID}, connect.CodeNotFound},
		{"the chat read times out", failingStreamRepo{chatErr: context.DeadlineExceeded}, &pb.StreamUserUpdatesRequest{SubscribeChatId: &chatID}, connect.CodeDeadlineExceeded},
		{"the chat read fails opaquely", failingStreamRepo{chatErr: errors.New("dsn=postgres://u:secret@h")}, &pb.StreamUserUpdatesRequest{SubscribeChatId: &chatID}, connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, client := streaming.NewTestStreamingServer(t,
				streaming.WithStreamingDeps(streaming.Deps{Repo: tc.repo}),
				streaming.WithPrincipal(testkit.WithUserID("test-user")))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stream, err := client.StreamUserUpdates(ctx, connect.NewRequest(tc.req))
			require.NoError(t, err)
			defer func() { _ = stream.Close() }()
			for stream.Receive() {
				// Drain the frames sent before the failure.
			}
			err = stream.Err()
			require.Equal(t, tc.want, connect.CodeOf(err), "err = %v", err)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}
