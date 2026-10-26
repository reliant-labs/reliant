package hosteduse

import (
	"context"
	"errors"
	"strings"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
)

type brokenStore struct{}

func (brokenStore) CreateHostedHTTPActionTicket(context.Context, core.HostedHTTPActionTicket) (bool, error) {
	return false, errors.New("synthetic-sensitive-backend-error")
}
func (brokenStore) RedeemHostedHTTPActionTicket(context.Context, string, string, string) (*core.HostedHTTPActionTicket, error) {
	return nil, errors.New("synthetic-sensitive-backend-error")
}

func TestPersistenceErrorsAreMetadataOnly(t *testing.T) {
	c := newTestClient(t, brokenStore{}, "https://controller.example")
	actionTest(t, "Action", func(ctx context.Context) error {
		_, err := c.Issue(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", sampleRequest)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-sensitive")
		return nil
	})
	_, err := Redeem(context.Background(), brokenStore{}, RedeemRequest{"ticket", "grant", strings.Repeat("a", 64)})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-sensitive")
}

func TestSignerFailureNeverPersistsAuthority(t *testing.T) {
	store := &memoryStore{}
	service, err := New(Deps{Store: store, Worker: &configv1.WorkerConfig{HostedActionsUrl: "https://controller.example"}, Sign: func() (string, error) { return "", errors.New("synthetic-sensitive-signing-error") }})
	require.NoError(t, err)
	actionTest(t, "Action", func(ctx context.Context) error {
		_, err := service.ExecuteAction(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", sampleRequest)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-sensitive")
		return nil
	})
	require.Empty(t, store.tickets)
}
