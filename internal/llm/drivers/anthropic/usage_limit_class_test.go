// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/stretchr/testify/assert"
)

// "AI provider usage limit reached (Anthropic, 5-hour window, overage
// unavailable: out of credits)" was logged at ERROR by four layers of the
// worker in prod. The user's subscription window is theirs to wait out.
func TestUsageLimitError_IsAUserError(t *testing.T) {
	limit := &ratelimit.UsageLimitError{Provider: "Anthropic", StatusCode: http.StatusTooManyRequests, RetryAfter: 3 * time.Hour, ResetAt: time.Now().Add(3 * time.Hour), Err: errors.New("429 Too Many Requests")}
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("LLM streaming error: %w", limit)))
}
