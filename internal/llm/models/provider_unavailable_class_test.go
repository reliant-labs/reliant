// Copyright (c) 2025 Reliant Labs
package models

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/stretchr/testify/assert"
)

// "failed to resolve model: none of required providers [codex] available" —
// the user's provider is not connected. Theirs to fix, in either shape the
// resolver produces it.
func TestNoServableProvider_IsAUserError(t *testing.T) {
	pinned := &ProviderUnavailableError{ModelID: "gpt-5.6-sol", Providers: []string{"codex"}}
	byTags := fmt.Errorf("no available provider for models with tags: [flagship] (tried 31 candidates): %w", drivererrors.ErrNoServableProvider)

	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("failed to resolve model: %w", pinned)))
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(byTags))
	assert.True(t, errors.Is(pinned, drivererrors.ErrNoServableProvider))
	assert.True(t, errors.Is(byTags, drivererrors.ErrNoServableProvider))
}
