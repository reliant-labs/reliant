// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
)

// GitHub answers a malformed token with 400, a revoked one with 401 and an
// account without a Copilot seat with 403. All three are the credential being
// refused, and must say so; a 5xx or 429 is an outage and must not.
func TestModelsStatusError_ClassifiesCredentialRefusals(t *testing.T) {
	t.Parallel()
	// The live answer to `Authorization: Bearer oauth` (chat dfd85515).
	badlyFormatted := []byte("bad request: Authorization header is badly formatted\n")
	assert.ErrorIs(t, modelsStatusError(400, badlyFormatted), registry.ErrCredentialRejected)
	assert.Contains(t, modelsStatusError(400, badlyFormatted).Error(), "Authorization header is badly formatted")
	assert.ErrorIs(t, modelsStatusError(401, []byte("Bad credentials")), registry.ErrCredentialRejected)
	assert.ErrorIs(t, modelsStatusError(403, []byte("no copilot seat")), registry.ErrCredentialRejected)

	for _, status := range []int{429, 500, 502, 503} {
		err := modelsStatusError(status, []byte("try later"))
		assert.Error(t, err)
		assert.NotErrorIs(t, err, registry.ErrCredentialRejected, "status %d is an outage", status)
	}
}
