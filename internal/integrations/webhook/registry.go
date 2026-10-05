// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/reliant-labs/reliant/internal/triggers"
)

// Registry holds the integrations this server receives events from: the
// providers that push (an app-level webhook) and the pollers that pull.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	pollers   map[string]triggers.Poller
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}, pollers: map[string]triggers.Poller{}}
}

// RegisterPoller adds a polled integration. A poller's triggers get a
// Temporal Schedule that calls it (see triggers.Poller for the contract).
func (r *Registry) RegisterPoller(integration string, p triggers.Poller) error {
	if p == nil {
		return fmt.Errorf("webhook: nil poller")
	}
	if !providerIDPattern.MatchString(integration) {
		return fmt.Errorf("webhook: integration id %q must match %s", integration, providerIDPattern)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pollers[integration]; exists {
		return fmt.Errorf("webhook: poller %q is already registered", integration)
	}
	r.pollers[integration] = p
	return nil
}

// Poller returns the poller registered for integration.
func (r *Registry) Poller(integration string) (triggers.Poller, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pollers[integration]
	return p, ok
}

// IsPolled reports whether integration is polled.
func (r *Registry) IsPolled(integration string) bool {
	_, ok := r.Poller(integration)
	return ok
}

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Register adds a provider. Registering two with one id is an error: the
// route is per id, and a silent replacement would send one provider's
// deliveries to another's verifier.
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return fmt.Errorf("webhook: nil provider")
	}
	id := p.ID()
	if !providerIDPattern.MatchString(id) {
		return fmt.Errorf("webhook: provider id %q must match %s", id, providerIDPattern)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[id]; exists {
		return fmt.Errorf("webhook: provider %q is already registered", id)
	}
	r.providers[id] = p
	return nil
}

// Provider returns the provider registered under id.
func (r *Registry) Provider(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// HasInboundSource reports whether integration delivers events here. It is
// what TriggerService checks before storing an integration trigger.
func (r *Registry) HasInboundSource(integration string) bool {
	_, pushed := r.Provider(integration)
	return pushed || r.IsPolled(integration)
}

// UserConfiguredURL reports whether integration's webhook is one each user
// points their own resources at (see UserConfigured), so its trigger should
// show the user the URL.
func (r *Registry) UserConfiguredURL(integration string) bool {
	p, ok := r.Provider(integration)
	if !ok {
		return false
	}
	uc, ok := p.(UserConfigured)
	return ok && uc.UserConfiguresWebhook()
}

// IDs lists the registered provider ids, sorted.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
