// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

// Spent Reliant credit is the user's to top up.
func TestReliantManagedQuotaExhausted_IsAUserError(t *testing.T) {
	exhausted := &ErrReliantManagedQuotaExhausted{Message: "You're out of Reliant credit."}
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("stream: %w", exhausted)))
}
