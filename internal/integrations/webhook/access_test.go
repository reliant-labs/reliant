// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A resource-keyed event (a GitHub event about a repository) routes ONLY by
// fresh access grants: one org installation reaches many users, and each
// user's triggers see only the repositories that user can see. The
// connection-account path is not consulted for it at all — an installation
// in common is not access to a repository.
func TestResourceKeyedEventsRouteOnlyByAccess(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	alice := integrationTrigger(t, "alice-issues", "alice", core.IntegrationConfig{Events: []string{"issues.*"}}, "")
	bob := integrationTrigger(t, "bob-issues", "bob", core.IntegrationConfig{Events: []string{"issues.opened"}}, "")
	carol := integrationTrigger(t, "carol-issues", "carol", core.IntegrationConfig{Events: []string{"issues.opened"}}, "")
	aliceOtherRepo := integrationTrigger(t, "alice-other", "alice", core.IntegrationConfig{
		Events: []string{"issues.opened"}, Match: map[string]string{"repository": "acme/other"},
	}, "")
	// Every trigger's CONNECTION covers installation 100 — the account path
	// alone would route all of them.
	for _, tr := range []*core.Trigger{alice, bob, carol, aliceOtherRepo} {
		env.route(tr, "100", core.ConnectionStatusActive)
	}
	env.store.grantAccess("alice", "100", "repo-1", "11")
	env.store.grantAccess("bob", "100", "repo-1", "22")
	// carol is in the installation but cannot see repo-1.
	env.store.grantAccess("carol", "100", "repo-2", "33")

	ev := issueOpened("delivery-1", "100", "acme/app")
	ev.Resource = "repo-1"
	rec := env.deliver(t, ev)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"alice-issues", "bob-issues"}, env.intake.triggerIDs(),
		"carol cannot see the repository, and alice's other trigger matches another repository")
	for _, a := range env.intake.all() {
		assert.Equal(t, "repo-1", a.Event.Payload["resource"])
	}
}

// Revocations in a delivery are applied BEFORE its events route, so a
// repository removed from an installation stops routing in the same request.
func TestRevocationsApplyBeforeRouting(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	alice := integrationTrigger(t, "alice-issues", "alice", core.IntegrationConfig{Events: []string{"*"}}, "")
	env.route(alice, "100", core.ConnectionStatusActive)
	env.store.grantAccess("alice", "100", "repo-1", "11")

	ev := issueOpened("delivery-1", "100", "acme/app")
	ev.Resource = "repo-1"
	ev.Revoke = []core.IntegrationAccessRevocation{{AccountKey: "100", ResourceKey: "repo-1"}}
	rec := env.deliver(t, ev)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, env.intake.all())
	assert.Equal(t, []core.IntegrationAccessRevocation{{AccountKey: "100", ResourceKey: "repo-1"}}, env.store.revocations)
}

// A revocation that names neither an account nor a subject would wipe every
// user's access; the receiver refuses to apply it (and still acks).
func TestUnscopedRevocationIsIgnored(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.store.grantAccess("alice", "100", "repo-1", "11")
	ev := TestDelivery{Revoke: []core.IntegrationAccessRevocation{{ResourceKey: "repo-1"}}}
	rec := env.deliver(t, ev)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, env.store.revocations)
}

// Events without a resource keep the connection-account routing.
func TestAccountKeyedEventsStillRouteByConnection(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"issues.opened"}}, ""), "100", core.ConnectionStatusActive)
	rec := env.deliver(t, issueOpened("d", "100", "acme/app"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"t1"}, env.intake.triggerIDs())
	assert.Zero(t, env.store.accessQueries)
}
