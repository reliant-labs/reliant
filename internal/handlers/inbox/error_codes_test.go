// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// What a store failure tells the caller: a classified error keeps its code,
// and an unclassified one is "failed to list inbox" with its own text
// withheld.
func TestInboxStoreErrorCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		code connect.Code
		msg  string
	}{
		"not found": {svcerr.NotFound("inbox"), connect.CodeNotFound, "inbox not found"},
		"deadline":  {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"canceled":  {context.Canceled, connect.CodeCanceled, ""},
		"opaque":    {errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal, "failed to list inbox"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := &MockRepository{
				ListWorktreeRecoveriesFunc: func(context.Context, string, string) ([]*core.WorktreeRecovery, error) { return nil, nil },
				ListInboxPendingFunc:       func(context.Context, string) ([]*core.InboxPending, error) { return nil, tc.err },
				// The other required read succeeds, so the pending read's
				// error is the one the caller sees.
				ListTriggersFunc: func(context.Context, core.TriggerFilters) ([]*core.Trigger, error) { return nil, nil },
			}
			svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}), WithPlaceholderDeps())
			_, err := svc.ListInbox(AuthedContext(t), connect.NewRequest(&reliantv1.ListInboxRequest{}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if tc.msg != "" {
				assert.Equal(t, tc.msg, msg)
			}
		})
	}
}

// Over a real Connect client, through the svcerr interceptor.
func TestInboxErrorsOnTheWire(t *testing.T) {
	t.Parallel()

	repo := &MockRepository{ListInboxPendingFunc: func(context.Context, string) ([]*core.InboxPending, error) {
		return nil, errors.New("dsn=postgres://u:secret@h")
	}}
	_, client := NewTestInboxServer(t, WithInboxDeps(Deps{Repo: repo}), WithPlaceholderDeps())
	_, err := client.ListInbox(context.Background(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
	assert.Equal(t, "failed to list inbox", svcerr.ToConnect(err).Message())
}
