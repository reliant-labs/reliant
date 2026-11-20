package streaming_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"

	streaming "github.com/reliant-labs/reliant/internal/handlers/streaming"
)

// failingStreamRepo fails the user log read with logErr and the chat read
// with chatErr. Everything else is unreachable: any other read fails with the
// mock's "Func not set" error.
func failingStreamRepo(logErr, chatErr error) *streaming.MockRepository {
	return &streaming.MockRepository{
		GetLatestUserUpdateSequenceFunc: func(context.Context, string) (int64, error) { return 0, logErr },
		GetChatRowFunc:                  func(context.Context, string) (*db.ChatRow, error) { return nil, chatErr },
	}
}

// Over the wire, a stream refused by a repository failure reports a
// classified error's own code and redacts an opaque one. A missing chat stays
// NotFound, which the web's chat stream reader takes as permanent; an outage
// is Internal, which it retries, instead of the NotFound it used to be.
func TestStreams_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	userStream := func(ctx context.Context, client reliantv1connect.StreamingServiceClient) error {
		stream, err := client.StreamUserUpdates(ctx, &pb.StreamUserUpdatesRequest{})
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		var streamErr error
		for {
			_, err := stream.Receive()
			if err != nil {
				streamErr = err
				break
			}
			// Drain the frames sent before the failure.
		}
		if errors.Is(streamErr, io.EOF) {
			streamErr = nil
		}
		return streamErr
	}
	chatStream := func(ctx context.Context, client reliantv1connect.StreamingServiceClient) error {
		stream, err := client.StreamChatUpdates(ctx, &pb.StreamChatUpdatesRequest{ChatId: "chat-1"})
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		var streamErr error
		for {
			_, err := stream.Receive()
			if err != nil {
				streamErr = err
				break
			}
		}
		if errors.Is(streamErr, io.EOF) {
			streamErr = nil
		}
		return streamErr
	}

	for _, tc := range []struct {
		name string
		repo *streaming.MockRepository
		open func(context.Context, reliantv1connect.StreamingServiceClient) error
		want connect.Code
	}{
		{"the user log times out", failingStreamRepo(context.DeadlineExceeded, nil), userStream, connect.CodeDeadlineExceeded},
		{"the user log fails opaquely", failingStreamRepo(errors.New("dsn=postgres://u:secret@h"), nil), userStream, connect.CodeInternal},
		{"the chat is missing", failingStreamRepo(nil, svcerr.NotFound("chat")), chatStream, connect.CodeNotFound},
		{"the chat read times out", failingStreamRepo(nil, context.DeadlineExceeded), chatStream, connect.CodeDeadlineExceeded},
		{"the chat read fails opaquely", failingStreamRepo(nil, errors.New("dsn=postgres://u:secret@h")), chatStream, connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, client := streaming.NewTestStreamingServer(t,
				streaming.WithStreamingDeps(streaming.Deps{Repo: tc.repo}),
				streaming.WithPrincipal(testkit.WithUserID("test-user")))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := tc.open(ctx, client)
			require.Equal(t, tc.want, connect.CodeOf(err), "err = %v", err)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}
