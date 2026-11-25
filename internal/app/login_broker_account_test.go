package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/controlplane"
)

// /auth/session reports the signed-in person's control-plane account — the
// fields onboarding decides from — and only that person's: an account the
// control plane answers for someone else, or one it cannot read, is reported
// as none, and the session itself is unaffected.
func TestSessionReportsTheControlPlaneAccount(t *testing.T) {
	created := time.Date(2025, time.March, 4, 5, 6, 7, 0, time.UTC)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := func(a *controlplane.Account, err error) AccountReader {
		return func(_ context.Context, token string) (*controlplane.Account, error) {
			require.Equal(t, "the-token", token, "the account is read with the person's own token")
			return a, err
		}
	}

	// The account id is control-plane's own; its external id is the
	// identity's subject.
	b := &loginBroker{logger: logger, accounts: reader(&controlplane.Account{
		ID: "cp-uuid-1", ExternalID: "user-1", Email: "p@example.com", Name: "P", CreatedAt: created, OnboardingCompleted: true,
	}, nil)}
	require.Equal(t, &sessionAccount{
		ID: "cp-uuid-1", Email: "p@example.com", Name: "P", CreatedAt: "2025-03-04T05:06:07Z", OnboardingCompleted: true,
	}, b.account(context.Background(), "the-token", "user-1"))

	b.accounts = reader(&controlplane.Account{ID: "cp-uuid-2", ExternalID: "user-2"}, nil)
	require.Nil(t, b.account(context.Background(), "the-token", "user-1"), "another person's account is never reported")

	// No external id in the answer: the control plane resolved the caller
	// from their own token, which is the binding.
	b.accounts = reader(&controlplane.Account{ID: "cp-uuid-1"}, nil)
	require.NotNil(t, b.account(context.Background(), "the-token", "user-1"))

	b.accounts = reader(nil, errors.New("control plane down"))
	require.Nil(t, b.account(context.Background(), "the-token", "user-1"))

	b.accounts = nil
	require.Nil(t, b.account(context.Background(), "the-token", "user-1"), "no control plane: no account")
}
