// Copyright (c) 2025 Reliant Labs
package codex

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
)

// A 400/401/403 from /codex/models — past the refresh transport, so not an
// expired token — is the backend refusing the credential every request carries.
// A transport error or 5xx is an outage.
func TestModelsRequestError_ClassifiesCredentialRefusals(t *testing.T) {
	t.Parallel()
	// The body chatgpt.com answers an unparseable token with.
	unparseable := `{"message": "Could not parse your authentication token. Please try signing in again.", "code": "unauthorized_unknown"}`
	err := modelsRequestError(codexAPIErrorWithBody(t, 401, unparseable))
	require.ErrorIs(t, err, registry.ErrCredentialRejected)
	var rejected *registry.CredentialRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, 401, rejected.Status)

	assert.ErrorIs(t, modelsRequestError(codexAPIErrorWithBody(t, 403, `{"detail":"forbidden"}`)), registry.ErrCredentialRejected)
	assert.ErrorIs(t, modelsRequestError(codexAPIErrorWithBody(t, 400, `{"detail":"bad"}`)), registry.ErrCredentialRejected)

	for _, status := range []int{429, 500, 502} {
		err := modelsRequestError(codexAPIErrorWithBody(t, status, `{"detail":"later"}`))
		assert.NotErrorIs(t, err, registry.ErrCredentialRejected, "status %d is an outage", status)
	}
	transport := modelsRequestError(fmt.Errorf("dial tcp: %w", errors.New("connection refused")))
	assert.NotErrorIs(t, transport, registry.ErrCredentialRejected)
	assert.Contains(t, transport.Error(), "codex models request failed")
}
