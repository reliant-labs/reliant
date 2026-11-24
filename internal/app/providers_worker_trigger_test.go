// Copyright (c) 2025 Reliant Labs

package app

import (
	"encoding/base64"
	"log/slog"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/credentials/automationtokens"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/triggerowner"
	"github.com/reliant-labs/reliant/internal/vault"
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

// On a hosted control plane trigger.New requires the automation-token store:
// without it the worker crash-looped at boot in prod (v1.32.0-rc.5, "trigger:
// Deps.AutomationKeys is required with Deps.ControlPlane").
func TestWorkerTriggerOnAHostedControlPlaneIsBuiltWithAutomationKeys(t *testing.T) {
	repo := db.NewTestRepo(t)
	lazy, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: "reliant"})
	require.NoError(t, err)
	t.Cleanup(lazy.Close)
	stores := triggerowner.NewStores(repo.ORM())
	owners, err := triggerowner.New(triggerowner.Deps{Tx: repo.ORM(), Locks: stores, Triggers: stores, Offers: stores, OrgReads: stores})
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	require.NoError(t, err)
	sealer := vault.New(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)})
	keys, err := automationtokens.New(automationtokens.Deps{DB: repo.ORM(), Sealer: sealer})
	require.NoError(t, err)

	wt, err := NewWorkerTrigger(WorkerTriggerDeps{
		Logger:         slog.New(slog.DiscardHandler),
		Config:         &config.Config{},
		DB:             repo,
		Temporal:       &TemporalClient{Client: lazy, Namespace: "reliant"},
		Vault:          sealer,
		Pollers:        &webhook.Registry{},
		Integrations:   &IntegrationSettings{Config: &configv1.IntegrationsConfig{}},
		ControlPlane:   &configv1.ControlPlaneConfig{Url: "https://admin.example.test", InternalServiceSecret: "test-secret"},
		AutomationKeys: keys,
		Owners:         owners,
	})
	require.NoError(t, err)
	require.NotNil(t, wt.Service)
}
