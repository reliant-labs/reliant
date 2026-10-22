package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A message read the client abandoned is the client's cancellation, not a
// server fault.
func TestLoadMessagesError(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithTimeout(context.Background(), 0)
	defer cancelExpired()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want connect.Code
	}{
		{"wrapped context canceled", context.Background(), fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"driver error after the request context was canceled", canceled, errors.New("pq: canceling statement due to user request"), connect.CodeCanceled},
		{"deadline exceeded", context.Background(), fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"expired request context", expired, errors.New("driver: bad connection"), connect.CodeDeadlineExceeded},
		{"genuine failure", context.Background(), errors.New("relation does not exist: dsn=postgres://u:secret@h"), connect.CodeInternal},
		{"a store miss", context.Background(), svcerr.NotFound("thread"), connect.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := messagesReadError(tc.ctx, tc.err)
			assert.Equal(t, tc.want, svcerr.Code(err))
			assert.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
		})
	}
}
