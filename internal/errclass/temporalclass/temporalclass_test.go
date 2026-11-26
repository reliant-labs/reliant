// Copyright (c) 2025 Reliant Labs
package temporalclass_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/errclass/temporalclass"
)

// TestRuleIsRegistered pins the init-time registration: importing temporalclass
// is enough for errclass.Classify and errclass.Library to read Temporal errors.
func TestRuleIsRegistered(t *testing.T) {
	benign := temporal.NewApplicationErrorWithOptions("AI provider usage limit reached", "ProviderUsageLimit",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
	plain := temporal.NewApplicationErrorWithOptions("nil map", "TerminalError", temporal.ApplicationErrorOptions{NonRetryable: true})

	cases := []struct {
		name    string
		err     error
		want    svcerr.Class
		library svcerr.Class
	}{
		{"nil", nil, svcerr.ClassNone, svcerr.ClassNone},
		// What the workflow side sees after the activity boundary.
		{"benign application error", benign, svcerr.ClassUser, svcerr.ClassUser},
		{"benign application error, wrapped", fmt.Errorf("activity failed: %w", benign), svcerr.ClassUser, svcerr.ClassUser},
		{"unspecified application error", plain, svcerr.ClassServer, svcerr.ClassNone},
		{"temporal canceled", temporal.NewCanceledError("stopped"), svcerr.ClassCanceled, svcerr.ClassCanceled},
		// svcerr's own verdicts are unchanged.
		{"svcerr user kind", svcerr.FailedPrecondition("no machine"), svcerr.ClassUser, svcerr.ClassNone},
		{"raw error", errors.New("pq: connection refused"), svcerr.ClassServer, svcerr.ClassNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, errclass.Classify(tc.err))
			assert.Equal(t, tc.library, errclass.Library(tc.err))
			assert.Equal(t, tc.want == svcerr.ClassServer, errclass.IsServerError(tc.err))
		})
	}
}

// TestCategory_RoundTrips pins the bridge: an error marked as a user error in
// the activity becomes Benign, and Benign reads back as a user error on the
// workflow side, where no Go type survives.
func TestCategory_RoundTrips(t *testing.T) {
	userErr := svcerr.WithClass(errors.New("your machine is not connected"), svcerr.ClassUser)
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, temporalclass.Category(userErr))
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, temporalclass.Category(context.Canceled))
	assert.Equal(t, temporal.ApplicationErrorCategoryUnspecified, temporalclass.Category(errors.New("nil map")))

	// Only the category crosses: rebuild the error the way the workflow
	// receives it, with no cause.
	crossed := temporal.NewApplicationErrorWithOptions(userErr.Error(), "TerminalError",
		temporal.ApplicationErrorOptions{Category: temporalclass.Category(userErr)})
	assert.Equal(t, svcerr.ClassUser, errclass.Classify(crossed))
}
