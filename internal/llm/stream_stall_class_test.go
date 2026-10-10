// Copyright (c) 2025 Reliant Labs
package llm

import (
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

// A stall with no content block open is, as observed, a subscription with no
// remaining credit: the user's to fix. A stall mid-block says nothing about
// the user, so it keeps the default — a server fault.
func TestStreamStallError_Class(t *testing.T) {
	awaiting := &StreamStallError{Phase: StallAwaitingContent, Timeout: 5 * time.Minute}
	midBlock := &StreamStallError{Phase: StallMidBlock, Timeout: 30 * time.Minute}

	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("failed to stream LLM response: %w", awaiting)))
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(fmt.Errorf("failed to stream LLM response: %w", midBlock)))
	assert.ErrorIs(t, awaiting, ErrStreamContentStalled)
}
