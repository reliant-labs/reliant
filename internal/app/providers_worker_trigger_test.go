// Copyright (c) 2025 Reliant Labs

package app

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/triggerowner"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The worker's trigger service is built with the trigger owner, which
// trigger.New requires: without it the worker refused to boot ("trigger:
// Deps.Owners is required") once the owner invariants left the db facade.
func TestWorkerTriggerIsBuiltWithTheTriggerOwner(t *testing.T) {
	repo := db.NewTestRepo(t)
	lazy, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: "reliant"})
	require.NoError(t, err)
	t.Cleanup(lazy.Close)
	stores := triggerowner.NewStores(repo.ORM())
	owners, err := triggerowner.New(triggerowner.Deps{Tx: repo.ORM(), Locks: stores, Triggers: stores, Offers: stores, OrgReads: stores})
	require.NoError(t, err)

	wt, err := NewWorkerTrigger(WorkerTriggerDeps{
		Logger:       slog.New(slog.DiscardHandler),
		Config:       &config.Config{},
		DB:           repo,
		Temporal:     &TemporalClient{Client: lazy, Namespace: "reliant"},
		Pollers:      &webhook.Registry{},
		Integrations: &IntegrationSettings{Config: &configv1.IntegrationsConfig{}},
		ControlPlane: &configv1.ControlPlaneConfig{},
		Owners:       owners,
	})
	require.NoError(t, err)
	require.NotNil(t, wt.Service)
}
