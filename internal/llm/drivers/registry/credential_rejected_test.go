// Copyright (c) 2025 Reliant Labs
package registry

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An account catalog's 400/401/403 is a verdict on the credential; every other
// status is an outage the caller fails open on.
func TestRejectedCredential_OnlyAuthStatusesAreVerdicts(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403} {
		err := RejectedCredential(status, "  bad request:\n Authorization header   is badly formatted ")
		require.Error(t, err, "status %d", status)
		assert.ErrorIs(t, fmt.Errorf("wrapped: %w", err), ErrCredentialRejected)
		var rejected *CredentialRejectedError
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, status, rejected.Status)
		assert.Equal(t, "bad request: Authorization header is badly formatted", rejected.Detail,
			"detail is whitespace-collapsed")
	}
	for _, status := range []int{200, 404, 408, 409, 429, 500, 502, 503, 504} {
		assert.NoError(t, RejectedCredential(status, "x"), "status %d is not a credential verdict", status)
	}
}

func TestRejectedCredential_DetailIsBounded(t *testing.T) {
	t.Parallel()
	var rejected *CredentialRejectedError
	require.True(t, errors.As(RejectedCredential(401, strings.Repeat("é", 1000)), &rejected))
	assert.LessOrEqual(t, len([]rune(rejected.Detail)), maxRejectionDetail+1)

	require.True(t, errors.As(RejectedCredential(403, ""), &rejected))
	assert.Equal(t, "Forbidden", rejected.Detail, "an empty body falls back to the status text")
}
