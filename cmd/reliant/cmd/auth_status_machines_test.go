// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/reliant/internal/cliauth"
)

// TL-46: `auth status` said "Logged in" for a login that `machine list` then
// refused, with nothing saying the login lacks daemon:manage.
func TestAuthStatus_SaysWhenTheLoginCannotManageMachines(t *testing.T) {
	isolateCLI(t)
	store := func(scopes ...string) {
		t.Helper()
		_, err := cliauth.Store(testServerURL, credentials.Credential{Token: "rlat_SCOPED00000000", TokenPrefix: "rlat_SCOPED", CreatedAt: fixtureTime, Scopes: scopes})
		require.NoError(t, err)
	}

	store(cliauth.ScopeAPI)
	out, err := executeRoot(t, "auth", "status", "--server", testServerURL)
	require.NoError(t, err, out)
	require.Contains(t, out, "this login cannot list or manage machines (it lacks daemon:manage)")
	out, err = executeRoot(t, "auth", "status", "--server", testServerURL, "--json")
	require.NoError(t, err, out)
	require.Contains(t, out, `"can_manage_machines": false`)

	store(cliauth.ScopeAPI, cliauth.ScopeMachines)
	out, err = executeRoot(t, "auth", "status", "--server", testServerURL)
	require.NoError(t, err, out)
	require.NotContains(t, out, "cannot list or manage machines")

	// A login that records no scopes says nothing either way.
	loginFor(t, testServerURL, "rlat_OLDLOGIN0000000")
	out, err = executeRoot(t, "auth", "status", "--server", testServerURL, "--json")
	require.NoError(t, err, out)
	require.NotContains(t, out, "can_manage_machines")
}
