// Copyright (c) 2025 Reliant Labs
package errclass

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"
)

func TestClassify(t *testing.T) {
	benign := temporal.NewApplicationErrorWithOptions("AI provider usage limit reached", "ProviderUsageLimit",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
	plain := temporal.NewApplicationErrorWithOptions("nil map", "TerminalError", temporal.ApplicationErrorOptions{NonRetryable: true})

	cases := []struct {
		name string
		err  error
		want svcerr.Class
	}{
		{"nil", nil, svcerr.ClassNone},
		// What the workflow side sees after the activity boundary.
		{"benign application error", benign, svcerr.ClassUser},
		{"benign application error, wrapped", fmt.Errorf("activity failed: %w", benign), svcerr.ClassUser},
		{"unspecified application error", plain, svcerr.ClassServer},
		{"temporal canceled", temporal.NewCanceledError("stopped"), svcerr.ClassCanceled},
		// Everything svcerr already knows.
		{"svcerr user kind", svcerr.FailedPrecondition("no machine"), svcerr.ClassUser},
		{"marked user", svcerr.WithClass(errors.New("machine offline"), svcerr.ClassUser), svcerr.ClassUser},
		{"connect 4xx", connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), svcerr.ClassUser},
		{"context canceled", context.Canceled, svcerr.ClassCanceled},
		{"raw error", errors.New("pq: connection refused"), svcerr.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Classify(tc.err))
			assert.Equal(t, tc.want == svcerr.ClassServer, IsServerError(tc.err))
		})
	}
}

// TestTemporalCategory_RoundTrips pins the bridge: an error marked as a user
// error in the activity becomes Benign, and Benign reads back as a user error
// on the workflow side, where no Go type survives.
func TestTemporalCategory_RoundTrips(t *testing.T) {
	userErr := svcerr.WithClass(errors.New("your machine is not connected"), svcerr.ClassUser)
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, TemporalCategory(userErr))
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, TemporalCategory(context.Canceled))
	assert.Equal(t, temporal.ApplicationErrorCategoryUnspecified, TemporalCategory(errors.New("nil map")))

	// Only the category crosses: rebuild the error the way the workflow
	// receives it, with no cause.
	crossed := temporal.NewApplicationErrorWithOptions(userErr.Error(), "TerminalError",
		temporal.ApplicationErrorOptions{Category: TemporalCategory(userErr)})
	assert.Equal(t, svcerr.ClassUser, Classify(crossed))
}
