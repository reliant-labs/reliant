// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// What a store failure tells the caller: a classified error keeps its code
// and text, and an unclassified one is "database error" with its own text
// withheld.
func TestTriggerStoreErrorCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		code connect.Code
		msg  string
	}{
		"not found": {core.ErrTriggerNotFound, connect.CodeNotFound, "trigger not found"},
		"deadline":  {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"canceled":  {context.Canceled, connect.CodeCanceled, ""},
		"opaque":    {errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal, "database error"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := &MockRepository{GetTriggerFunc: func(context.Context, string) (*core.Trigger, error) { return nil, tc.err }}
			svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}), WithPlaceholderDeps())
			_, err := svc.GetTrigger(AuthedContext(t), &reliantv1.GetTriggerRequest{Id: "t-1"})
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.CodeV2(err))
			msg := svcerr.ToConnectV2(err).Message()
			assert.NotContains(t, msg, "secret")
			if tc.msg != "" {
				assert.Equal(t, tc.msg, msg)
			}
			assert.ErrorIs(t, err, tc.err, "the server keeps the cause")
		})
	}
}

// Another user's trigger is NotFound exactly like a missing one (the CLI's
// `trigger` commands key on the code), over a real Connect client.
func TestTriggerErrorsOnTheWire(t *testing.T) {
	t.Parallel()

	repo := &MockRepository{GetTriggerFunc: func(_ context.Context, id string) (*core.Trigger, error) {
		switch id {
		case "theirs":
			return &core.Trigger{ID: id, UserID: "someone-else"}, nil
		case "missing":
			return nil, core.ErrTriggerNotFound
		}
		return nil, errors.New("dsn=postgres://u:secret@h")
	}}
	_, client := NewTestTriggerServer(t, WithTriggerDeps(Deps{Repo: repo}), WithPlaceholderDeps())
	for _, id := range []string{"theirs", "missing"} {
		_, err := client.GetTrigger(context.Background(), &reliantv1.GetTriggerRequest{Id: id})
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, svcerr.CodeV2(err), id)
		assert.Equal(t, "trigger not found", svcerr.ToConnectV2(err).Message(), id)
	}
	_, err := client.GetTrigger(context.Background(), &reliantv1.GetTriggerRequest{Id: "broken"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.CodeV2(err))
	assert.Equal(t, "database error", svcerr.ToConnectV2(err).Message())
}

// A second trigger with a name the project already has is AlreadyExists with
// a remedy, not Internal "database error" (prod obs issue 84a072c8: a
// duplicate-key 23505 surfaced as an opaque 500).
func TestCreateTriggerNameTakenIsAlreadyExists(t *testing.T) {
	t.Parallel()
	err := triggerDBError("create trigger", fmt.Errorf("%w: %q", core.ErrTriggerNameTaken, "tick"))
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.CodeV2(err))
	assert.Contains(t, svcerr.ToConnectV2(err).Message(), "already exists")
}
