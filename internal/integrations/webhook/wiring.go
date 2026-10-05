// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Store is everything both receivers read. *db.Repo satisfies it.
type Store interface {
	HooksStore
	EventsStore
}

// Vault seals and opens webhook HMAC secrets. Satisfied by *vault.Vault.
type Vault interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// Inbound is the api-server's inbound-trigger surface: both receivers, plus
// what TriggerService needs to write inbound triggers.
type Inbound struct {
	PublicURL string
	Registry  *Registry
	Intake    Intake
	Vault     Vault

	hooks  *HooksReceiver
	events *EventsReceiver
}

// NewInbound wires the receivers. vault may be nil (no signed webhooks).
func NewInbound(store Store, intake Intake, registry *Registry, v Vault, publicURL string) *Inbound {
	in := &Inbound{PublicURL: strings.TrimSpace(publicURL), Registry: registry, Intake: intake, Vault: v}
	var opener SecretOpener
	if v != nil {
		opener = v
	}
	in.hooks = NewHooksReceiver(HooksOptions{Store: store, Intake: intake, Opener: opener})
	in.events = NewEventsReceiver(EventsOptions{Store: store, Intake: intake, Registry: registry, PublicURL: in.PublicURL})
	return in
}

// Register mounts every inbound route.
func (in *Inbound) Register(handle func(pattern string, handler http.Handler)) {
	in.hooks.Register(handle)
	in.events.Register(handle)
}

// RegistryFromEnv builds the provider registry this deployment receives for.
// Wave-1 providers (GitHub, Slack, Twilio) register here, each gated on its
// own deployment secret. The test provider is registered only when
// RELIANT_TEST_INTEGRATION_SECRET is set — an e2e stack, never production.
func RegistryFromEnv(getenv func(string) string) (*Registry, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	r := NewRegistry()
	if secret := strings.TrimSpace(getenv("RELIANT_TEST_INTEGRATION_SECRET")); secret != "" {
		if err := r.Register(NewTestProvider(secret)); err != nil {
			return nil, err
		}
		if err := r.RegisterPoller(TestPollerID, NewTestPoller()); err != nil {
			return nil, err
		}
	}
	return r, nil
}

var _ Store = (interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)
	ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error)
})(nil)
