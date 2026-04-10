// Copyright (c) 2025 Reliant Labs
package codex

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAugmentAPIError_nil(t *testing.T) {
	require.NoError(t, AugmentAPIError(nil))
}

func TestAugmentAPIError_passThrough(t *testing.T) {
	err := errors.New("some other failure")
	require.Equal(t, err, AugmentAPIError(err))
}

func TestAugmentAPIError_missingScope(t *testing.T) {
	orig := fmt.Errorf(`POST "https://chatgpt.com/backend-api/codex/responses": 401 Unauthorized {
    "message": "You have insufficient permissions for this operation. Missing scopes: api.responses.write.",
    "code": "missing_scope"
  }`)
	wrapped := AugmentAPIError(orig)
	require.ErrorIs(t, wrapped, ErrMissingResponsesWriteScope)
	require.ErrorContains(t, wrapped, orig.Error())
}
