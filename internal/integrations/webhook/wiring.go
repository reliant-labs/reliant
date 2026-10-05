// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
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
	// Access is the per-integration access refresher for providers whose
	// events are access-gated (Event.ResourceKey), keyed by integration id.
	// TriggerService refreshes the owner's access when a trigger of one is
	// activated. Set by the composition root; nil means none.
	Access map[string]AccessRefresher

	hooks  *HooksReceiver
	events *EventsReceiver
}

// AccessRefresher re-reads one user's access for an access-gated provider.
type AccessRefresher interface {
	Refresh(ctx context.Context, userID string) error
	// IsPermanent reports whether a Refresh error needs the user to act.
	IsPermanent(err error) bool
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
	if secret := strings.TrimSpace(getenv(SlackSigningSecretEnv)); secret != "" {
		if err := r.Register(NewSlackProvider(secret)); err != nil {
			return nil, err
		}
	}
	// GitHub: the one App's webhook. Without its secret nothing could be
	// verified, so the route stays 404 and integration triggers for github
	// are refused at write time.
	if secret := strings.TrimSpace(getenv(github.SecretEnv)); secret != "" {
		if err := r.Register(NewGitHubProvider(secret)); err != nil {
			return nil, err
		}
	}
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
	ListAccessRoutedTriggers(ctx context.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error)
	RevokeIntegrationAccess(ctx context.Context, integration string, rev core.IntegrationAccessRevocation) (int64, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)
	ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error)
})(nil)
