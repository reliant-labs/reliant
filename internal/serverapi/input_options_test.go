// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions/inputoptionstest"
)

var _ trigger.OptionsChecker = optionsChecker{}

func checkerOverGitHub(t *testing.T, without ...string) (optionsChecker, *inputoptionstest.Fake) {
	t.Helper()
	f := inputoptionstest.NewFake(t, map[string]*inputoptionstest.Account{
		"alice": {Installations: []inputoptionstest.Installation{{ID: 11, Account: "acme", Repos: []inputoptionstest.Repo{
			{FullName: "acme/web", Push: true}, {FullName: "acme/docs", Push: false},
		}}}},
	})
	return optionsChecker{svc: f.Service(without...)}, f
}

func repoCheck(identity core.ActingIdentity, values ...string) trigger.ValueCheck {
	return trigger.ValueCheck{UserID: "alice", Type: "github.repository", ActingIdentity: identity, Values: values}
}

func TestCheckValuesReachableValuesPass(t *testing.T) {
	c, _ := checkerOverGitHub(t)
	res, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityOwner, "acme/web", "acme/docs"))
	require.NoError(t, err)
	assert.Empty(t, res.Unreachable)
	assert.Empty(t, res.Warnings)
}

// A repository the owner cannot see (or that does not exist) is a definite
// "unreachable": the one answer that refuses an enable.
func TestCheckValuesUnreachable(t *testing.T) {
	c, _ := checkerOverGitHub(t)
	res, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityOwner, "acme/web", "acme/gone", "other/private"))
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/gone", "other/private"}, res.Unreachable)
	assert.Empty(t, res.Warnings)
}

// The bot cannot write where the owner cannot push: a warning, not a refusal.
func TestCheckValuesBotCannotPushIsAWarningOnly(t *testing.T) {
	c, _ := checkerOverGitHub(t)
	res, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityApp, "acme/web", "acme/docs"))
	require.NoError(t, err)
	assert.Empty(t, res.Unreachable, "acme/docs exists and is visible; the bot just cannot act in it")
	assert.Equal(t, []string{"acme/docs: The Reliant bot can only write where you have push access, so it will not act in this repository."}, res.Warnings)

	owner, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityOwner, "acme/docs"))
	require.NoError(t, err)
	assert.Empty(t, owner.Warnings, "as the owner the same value is fine")
}

// A check that cannot be made is not evidence about the value: the provider
// failing is an error, which the enable turns into a warning.
func TestCheckValuesUpstreamErrorIsAnErrorNotUnreachable(t *testing.T) {
	c, f := checkerOverGitHub(t)
	f.SetDown("alice", true)
	res, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityOwner, "acme/web", "acme/gone"))
	require.Error(t, err)
	assert.Empty(t, res.Unreachable, "nothing is called unreachable while GitHub is down")
}

func TestCheckValuesWithoutAConnectionIsAnErrorNotUnreachable(t *testing.T) {
	c, f := checkerOverGitHub(t, "alice")
	res, err := c.CheckValues(context.Background(), repoCheck(core.ActingIdentityOwner, "acme/web"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable connection")
	assert.Empty(t, res.Unreachable)
	assert.Empty(t, f.Requests())
}

// The connection the enable names is the one the check goes through, and it
// must be the owner's.
func TestCheckValuesNamedConnectionMustBeTheOwners(t *testing.T) {
	c, _ := checkerOverGitHub(t)
	check := repoCheck(core.ActingIdentityOwner, "acme/web")
	check.ConnectionID = "conn-alice"
	_, err := c.CheckValues(context.Background(), check)
	require.NoError(t, err)

	check.ConnectionID = "conn-mallory"
	_, err = c.CheckValues(context.Background(), check)
	require.Error(t, err)
	assert.Equal(t, "no such connection", err.Error(), "another user's connection reads like a missing one")
}

// A type with nothing to check against has nothing to refuse.
func TestCheckValuesUnknownTypeRefusesNothing(t *testing.T) {
	c, _ := checkerOverGitHub(t)
	res, err := c.CheckValues(context.Background(), trigger.ValueCheck{UserID: "alice", Type: "github.nothing", Values: []string{"x"}})
	require.NoError(t, err)
	assert.Empty(t, res.Unreachable)
}

// A scoped type is checked within its scope.
func TestCheckValuesLabelsWithinTheirRepository(t *testing.T) {
	f := inputoptionstest.NewFake(t, map[string]*inputoptionstest.Account{
		"alice": {Labels: map[string][]string{"acme/web": {"bug"}}},
	})
	c := optionsChecker{svc: f.Service()}
	res, err := c.CheckValues(context.Background(), trigger.ValueCheck{
		UserID: "alice", Type: "github.label", Scope: map[string]any{"repository": "acme/web"}, Values: []string{"bug", "brand-new"},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Unreachable, "a label that does not exist yet is allowed (free text)")

	_, err = c.CheckValues(context.Background(), trigger.ValueCheck{UserID: "alice", Type: "github.label", Values: []string{"bug"}})
	require.Error(t, err, "without its repository a label cannot be checked")
}
