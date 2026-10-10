// Copyright (c) 2025 Reliant Labs
package registry

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

// A provider refusing the user's credential is the user's to reconnect.
func TestCredentialRejected_IsAUserError(t *testing.T) {
	rejected := RejectedCredential(http.StatusBadRequest, "Authorization header is badly formatted")
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("copilot availability: %w", rejected)))
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("resolve: %w", ErrCredentialRejected)))
	assert.True(t, errors.Is(rejected, ErrCredentialRejected), "errors.Is must still match the sentinel")
}
