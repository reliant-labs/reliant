// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"errors"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"google.golang.org/genai"
)

// The request retries through the shared ladder (ratelimit.Retryer), as every
// driver's does: 408/409/429/5xx and dropped connections, the provider's wait
// honored up to the ladder's cap, and each wait published as EventRetryWait
// before it is slept. genai sends no Retry-After; the wait is in the error's
// google.rpc.RetryInfo, which DescribeGoogle reads along with the daily and
// zero quotas a wait cannot fix. genai itself retries nothing unless asked
// (a nil HTTPOptions.RetryOptions is one attempt), so nothing retries twice.

func (g *GeminiClient) retryer() ratelimit.Retryer {
	r := g.ladder
	if r.Provider == "" {
		r.Provider = "Gemini"
		r.Log = "Gemini request"
	}
	return r
}

// geminiFailure describes a failed genai call to the ladder. The status comes
// from genai.APIError (or, for an error sent in-band, its status name); a
// status-less error is retried only when it is a transport failure.
func geminiFailure(err error) ratelimit.Failure {
	f := ratelimit.Failure{Err: err}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		f.Fatal = !ratelimit.IsTransportError(err)
		return f
	}
	f.Status = ratelimit.GoogleHTTPStatus(apiErr.Code, apiErr.Status)
	f.Fatal = f.Status == 0
	ratelimit.DescribeGoogle(&f, apiErr.Details)
	return f
}
