// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"

	// github/pr.merge@1's executor, registered on import.
	_ "github.com/reliant-labs/reliant/internal/integrations/ghmerge"
	"github.com/reliant-labs/reliant/internal/integrations/gmail"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Store is everything both receivers read. *db.Repo satisfies it.
type Store interface {
	HooksStore
	EventsStore
}

// Vault seals and opens webhook HMAC secrets. Satisfied by vault.Service.
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

// WithConnectionSecrets lets the app-level receiver verify deliveries that
// are signed with a per-connection secret (Twilio's Auth Token). Without it,
// such providers answer 503.
func (in *Inbound) WithConnectionSecrets(secrets ConnectionSecrets) *Inbound {
	in.events.opts.ConnectionSecrets = secrets
	return in
}

// Register mounts every inbound route.
func (in *Inbound) Register(handle func(pattern string, handler http.Handler)) {
	in.hooks.Register(handle)
	in.events.Register(handle)
}

// RegistryConfig is what decides which integrations this deployment receives
// events for: each one's deployment secret (the server role's
// IntegrationsConfig) and its OAuth clients.
type RegistryConfig struct {
	// SlackSigningSecret verifies Slack deliveries; empty receives none.
	SlackSigningSecret string
	// GitHubWebhookSecret verifies the GitHub App's deliveries; empty
	// receives none.
	GitHubWebhookSecret string
	// OAuthClients are the deployment's integration OAuth clients; a polled
	// integration is registered only when its client is configured.
	OAuthClients connections.OAuthClients
	// PublicURL is this deployment's public base URL; Twilio signs it, so
	// Twilio is registered only when it parses.
	PublicURL string
	// TestIntegrationSecret registers the test provider and poller; only an
	// e2e stack sets it, never production.
	TestIntegrationSecret string
}

// RegistryFor builds the provider registry this deployment receives for.
// Wave-1 providers (GitHub, Slack, Twilio) register here, each gated on its
// own deployment secret; polled integrations (Gmail) on the OAuth client their
// connections need. The test provider is registered only when
// cfg.TestIntegrationSecret is set — an e2e stack, never production.
//
// The api-server and the worker both build it, so they agree on which
// integrations deliver events: the api-server refuses a trigger whose
// integration has no source, and the worker runs the polls.
func RegistryFor(cfg RegistryConfig) (*Registry, error) {
	r := NewRegistry()
	if secret := strings.TrimSpace(cfg.SlackSigningSecret); secret != "" {
		if err := r.Register(NewSlackProvider(secret)); err != nil {
			return nil, err
		}
	}
	// GitHub: the one App's webhook. Without its secret nothing could be
	// verified, so the route stays 404 and integration triggers for github
	// are refused at write time.
	if secret := strings.TrimSpace(cfg.GitHubWebhookSecret); secret != "" {
		if err := r.Register(NewGitHubProvider(secret)); err != nil {
			return nil, err
		}
	}
	// Gmail is polled, not pushed: there is no webhook to verify. It is
	// registered when the deployment configured the Google OAuth client,
	// without which no one can hold a Gmail connection to poll through.
	if gmailClientID(cfg.OAuthClients) != "" {
		m, err := catalog.MustBuiltin().Manifest(gmail.ID, 1)
		if err != nil {
			return nil, err
		}
		p, err := gmail.NewInstrumented(gmail.Deps{BaseURL: m.GetConnection().GetBaseUrl(), Runner: httpaction.NewRunner(netguard.New())})
		if err != nil {
			return nil, err
		}
		if err := r.RegisterPoller(gmail.ID, p); err != nil {
			return nil, err
		}
	}
	// Twilio: no deployment secret — each delivery is verified against its
	// account's Auth Token, from the users' own connections. What it does
	// need is PUBLIC_URL, because Twilio signs the URL itself.
	if parsePublicBase(cfg.PublicURL) != nil {
		if err := r.Register(NewTwilioProvider()); err != nil {
			return nil, err
		}
	}
	if secret := strings.TrimSpace(cfg.TestIntegrationSecret); secret != "" {
		if err := r.Register(NewTestProvider(secret)); err != nil {
			return nil, err
		}
		if err := r.RegisterPoller(TestPollerID, NewTestPoller()); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// gmailClientID is the deployment's Gmail OAuth client id, "" when it has none.
func gmailClientID(clients connections.OAuthClients) string {
	if clients == nil {
		return ""
	}
	id, _ := clients(gmail.ID)
	return strings.TrimSpace(id)
}

var _ Store = (interface {
	ListAccessRoutedTriggers(ctx context.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error)
	RevokeIntegrationAccess(ctx context.Context, integration string, rev core.IntegrationAccessRevocation) (int64, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)
	ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error)
})(nil)
