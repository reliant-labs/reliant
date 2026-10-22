// Copyright (c) 2025 Reliant Labs
package account

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Control-plane is a remote Connect client, so its error is this rpc's logged
// cause and never its verdict: the client reads the sentence DeleteAccount
// chose, with the code it chose, and none of control-plane's own words. Its
// Unavailable (a refund that could not be confirmed) still picks the
// retryable sentence. Nothing here reaches the local purge, so the database is
// a placeholder.
func TestDeleteAccount_ControlPlaneErrorsOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remote      error
		wantCode    connect.Code
		wantMessage string
	}{
		{
			name:        "refund not confirmed",
			remote:      connect.NewError(connect.CodeUnavailable, errors.New("stripe.internal:443: refund re_123 pending")),
			wantCode:    connect.CodeUnavailable,
			wantMessage: "we could not confirm your refund with our payment provider; nothing was deleted, please try again in a minute",
		},
		{
			name:        "any other control-plane failure",
			remote:      connect.NewError(connect.CodeNotFound, errors.New("billing-svc.internal:8443: no customer row")),
			wantCode:    connect.CodeInternal,
			wantMessage: "could not delete your platform account; nothing was deleted",
		},
		{
			name:        "a transport failure",
			remote:      errors.New("dial tcp 10.0.0.7:8443: connect: connection refused"),
			wantCode:    connect.CodeInternal,
			wantMessage: "could not delete your platform account; nothing was deleted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := &stubControlPlane{err: tc.remote}
			_, client := NewTestAccountServer(t, WithAccountDeps(Deps{ControlPlane: cp}), WithPlaceholderDeps())

			_, err := client.DeleteAccount(context.Background(), connect.NewRequest(&reliantv1.DeleteAccountRequest{ConfirmEmail: "test-user@example.test"}))
			var cerr *connect.Error
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tc.wantCode, cerr.Code())
			assert.Equal(t, tc.wantMessage, cerr.Message())
			assert.Equal(t, 1, cp.calls)
		})
	}
}
