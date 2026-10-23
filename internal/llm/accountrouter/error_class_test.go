// Copyright (c) 2025 Reliant Labs
package accountrouter

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Every account being rate-limited is the state of the user's subscriptions,
// not a server fault: the logger records it as a user error (WARN through
// this package's observe chain), never an ERROR or a Sentry event.
func TestErrAllLimitedIsAUserError(t *testing.T) {
	err := fmt.Errorf("assign: %w", &ErrAllLimited{Driver: "claude", EarliestReset: time.Now().Add(time.Hour)})
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
}
