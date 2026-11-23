// Copyright (c) 2026 Reliant Labs
package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// A failed provider call is classed from the most specific signal to the
// least, and every class is a value of telemetry's closed vocabulary.
func TestErrorType(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"nil":               {nil, ""},
		"canceled":          {fmt.Errorf("stream: %w", context.Canceled), telemetry.ErrorTypeCanceled},
		"deadline":          {fmt.Errorf("stream: %w", context.DeadlineExceeded), telemetry.ErrorTypeTimeout},
		"context overflow":  {&drivererrors.ContextOverflowError{Err: errors.New(`POST "https://api": 400 Bad Request`)}, telemetry.ErrorTypeContextOverflow},
		"overflow in words": {errors.New(`POST "https://api": 400 Bad Request {"error":{"code":"context_length_exceeded"}}`), telemetry.ErrorTypeContextOverflow},
		"credit exhausted":  {errors.New(`POST "https://api": 429 Too Many Requests {"error":{"code":"insufficient_quota"}}`), telemetry.ErrorTypeCreditExhausted},
		"prompt refused":    {errors.New(`POST "https://api": 400 Bad Request {"error":{"code":"invalid_prompt"}}`), telemetry.ErrorTypePromptRefused},
		"stream stalled":    {&StreamStallError{Phase: StallMidBlock}, telemetry.ErrorTypeStreamStalled},
		"stream ended":      {fmt.Errorf("read: %w", drivererrors.ErrStreamEndedEarly), telemetry.ErrorTypeStreamEndedEarly},
		"malformed output":  {fmt.Errorf("parse: %w", drivererrors.ErrMalformedModelOutput), telemetry.ErrorTypeMalformedOutput},
		"empty input":       {drivererrors.NewEmptyInputError("anthropic", 0, 0, 0), telemetry.ErrorTypeEmptyInput},
		"no provider":       {fmt.Errorf("route: %w", drivererrors.ErrNoServableProvider), telemetry.ErrorTypeNoProvider},
		"an HTTP status":    {errors.New(`POST "https://api": 529 Overloaded`), "http_529"},
		"a status in words": {errors.New("upstream returned status 503: try later"), "http_503"},
		"anything else":     {errors.New("connection reset by peer"), telemetry.ErrorTypeError},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := ErrorType(tc.err)
			assert.Equal(t, tc.want, got)
			if tc.err != nil {
				assert.True(t, telemetry.IsErrorType(got), "%q is in the vocabulary", got)
			}
		})
	}
}
