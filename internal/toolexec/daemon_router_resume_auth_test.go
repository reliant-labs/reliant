// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
)

// TestEnsureAwake_ResumeCarriesTheCallersBearer: waking a suspended daemon
// must authenticate as the user. control-plane's DaemonService/ResumeDaemon
// has no service-credential path — it derives the owner from the forwarded
// Bearer (svcdaemon.ownerForDaemon) and rejects a call without one — so a
// resume sent without the user's token could never succeed.
func TestEnsureAwake_ResumeCarriesTheCallersBearer(t *testing.T) {
	const userID = "user-resume-auth"
	auth.SetUserJWT(userID, "jwt-for-resume")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	router, resumer := newSuspendedRouter(t, userID)

	id, err := router.EnsureAwake(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-suspended", id)
	assert.Equal(t, []resumeCall{{token: "jwt-for-resume", daemonID: "daemon-suspended"}}, resumer.calls,
		"a suspended daemon must be resumed once, as the user")
}

// With no JWT and no credentials source, nothing can authenticate a wake, so
// no resume is attempted.
func TestEnsureAwake_WithoutAnyCredentialSendsNoResume(t *testing.T) {
	router, resumer := newSuspendedRouter(t, "user-signed-out")

	_, err := router.EnsureAwake(context.Background(), "user-signed-out", pinned)
	require.Error(t, err)
	assert.Empty(t, resumer.calls)
}
