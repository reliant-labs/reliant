package message_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	message "github.com/reliant-labs/reliant/internal/handlers/message"
)

// failingMessageRepo fails GetMessage with err. Every other method is
// unreachable: the embedded nil Repository panics if one is called.
type failingMessageRepo struct {
	message.Repository
	err error
}

func (r failingMessageRepo) GetMessage(context.Context, string) (*db.Message, error) {
	return nil, r.err
}

// GetMessage passes a classified repository error through and redacts an
// opaque one to its own summary.
func TestGetMessage_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a store miss is NotFound", svcerr.NotFound("message"), connect.CodeNotFound},
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := message.NewTestMessage(t, message.WithMessageDeps(message.Deps{Repo: failingMessageRepo{err: tc.err}}))
			_, err := svc.GetMessage(message.AuthedContext(t), connect.NewRequest(&pb.GetMessageRequest{MessageId: "msg-1"}))
			require.Equal(t, tc.want, svcerr.Code(err), "err = %v", err)
			require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
		})
	}
}
