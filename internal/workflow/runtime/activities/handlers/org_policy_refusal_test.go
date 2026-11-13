// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// An org policy refusal met by a routed API-key account is the org's verdict,
// not the account's: providerFailure hands it back unchanged, marks no
// credential (CredentialRejected is unaffected for a BYO key's own 403,
// TestCredentialRejection_*), benches no account and offers no failover. It is
// stamped attended or unattended from the call's scope.
func TestProviderFailure_PolicyRefusalIsAVerdict(t *testing.T) {
	a, b := apiKeyAccount("a", "alice@acme.com", 0), apiKeyAccount("b", "bob@acme.com", 1)
	withAvailableDrivers(t, driversWith(a, b))
	env := newRoutingEnv(t)
	marker := &markerFake{store: env.store}
	env.routing.Credentials = marker
	first := resolveOnAnthropic(t, env, "t")

	for _, attended := range []bool{true, false} {
		refusal := &orgpolicy.Refusal{Reason: orgpolicy.ReasonProviderNotAllowed}
		in := fmt.Errorf("stream llm response: %w", refusal)
		scope := drivers.Scope{UserID: "u1", OrgID: "org-1", Attended: attended}
		got := env.routing.providerFailure(context.Background(), in, usageLimitContext{
			Scope: scope, ThreadID: "t", Driver: "anthropic", ModelID: routeModel, Account: first.Account})
		require.Same(t, in, got, "attended=%v: the refusal comes back unchanged, not as a failover", attended)
		assert.Equal(t, !attended, refusal.Unattended)
	}
	assert.Empty(t, marker.marked, "no credential was marked")
	next := resolveOnAnthropic(t, env, "t2")
	assert.Equal(t, "a", next.Account.Config.AccountID, "the account was not benched")
}
