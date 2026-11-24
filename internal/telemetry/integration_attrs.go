// Copyright (c) 2025 Reliant Labs
package telemetry

// The names of the integration.call span (integration_spans.go) and its
// attributes. They live in their own file, beside ai_attrs.go, so the span
// families that are still being added do not all edit one const block. They are a
// public contract once released: never rename one in place, add the new name
// beside it.

// SpanNameIntegrationCall is the prefix of one integration action call's span:
// "integration.call <integration>.<operation>" (integration_spans.go).
const SpanNameIntegrationCall = "integration.call"

// The attributes of an integration call's span. They name the call and say
// how it ended, never what it sent or got back: no params, no body, no URL,
// and of the response's headers only the rate-limit ones.
const (
	// AttrIntegrationID is the integration (its manifest id: "github",
	// "http"), AttrIntegrationOperation the action ("pr.merge", "request")
	// and AttrIntegrationUses the reference both come from
	// ("github/pr.merge@1").
	AttrIntegrationID        = "reliant.integration.id"
	AttrIntegrationOperation = "reliant.integration.operation"
	AttrIntegrationUses      = "reliant.integration.uses"
	// AttrIntegrationOutcome is how the call ended; its values are the
	// IntegrationOutcome* constants. AttrIntegrationErrorCode is the failure's
	// code when it has one (a credential error's code, "http_404"), also
	// written as error.type.
	AttrIntegrationOutcome   = "reliant.integration.outcome"
	AttrIntegrationErrorCode = "reliant.integration.error_code"
	// AttrIntegrationRetryable is a failed call's retry verdict, as the
	// action's error rules (or the 429/5xx default) classified it.
	AttrIntegrationRetryable = "reliant.integration.retryable"
	// AttrIntegrationConnectionID is the saved connection the call was
	// authenticated with: an id, never the credential.
	AttrIntegrationConnectionID = "reliant.integration.connection.id"
	// AttrIntegrationPages is how many responses the call read (a paginated
	// walk or a fan-out reads several); AttrIntegrationTruncated is set when
	// the walk stopped at its page limit with more still to read.
	AttrIntegrationPages     = "reliant.integration.pages"
	AttrIntegrationTruncated = "reliant.integration.truncated"
	// AttrIntegrationHosted marks a hosted_use call, which the controller
	// makes with a granted credential instead of the worker's runner.
	AttrIntegrationHosted = "reliant.integration.hosted"
	// The provider's rate limit, from the last response's standard headers
	// (X-RateLimit-* or RateLimit-*, and Retry-After): the window's limit,
	// what remains and what is used of it, which budget it is (GitHub's
	// "core", "graphql"), when it resets (unix seconds) and how long the
	// provider asked to wait (seconds). Each is set only when the provider
	// sent it.
	AttrIntegrationRateLimitLimit      = "reliant.integration.rate_limit.limit"
	AttrIntegrationRateLimitRemaining  = "reliant.integration.rate_limit.remaining"
	AttrIntegrationRateLimitUsed       = "reliant.integration.rate_limit.used"
	AttrIntegrationRateLimitResource   = "reliant.integration.rate_limit.resource"
	AttrIntegrationRateLimitReset      = "reliant.integration.rate_limit.reset"
	AttrIntegrationRateLimitRetryAfter = "reliant.integration.rate_limit.retry_after"

	// AttrHTTPResponseStatusCode and AttrServerAddress are OpenTelemetry's
	// HTTP client conventions: the last response's status and the host it
	// came from (a host, never a URL).
	AttrHTTPResponseStatusCode = "http.response.status_code"
	AttrServerAddress          = "server.address"
)

// Values of AttrIntegrationOutcome.
const (
	// IntegrationOutcomeOK: the provider answered and the action succeeded.
	IntegrationOutcomeOK = "ok"
	// IntegrationOutcomeError: the provider answered with a failure (a
	// non-2xx status, or a 2xx an error rule reads as one).
	IntegrationOutcomeError = "error"
	// IntegrationOutcomeCredentialError: no credential could be had for the
	// call (not connected, needs reauthorizing), so nothing was sent.
	IntegrationOutcomeCredentialError = "credential_error" //nolint:gosec // G101: an outcome name, not a credential
	// IntegrationOutcomeRefused: a policy or grant refused the call before it
	// was sent (the org does not allow the integration, a hosted grant).
	IntegrationOutcomeRefused = "refused"
	// IntegrationOutcomeFailed: the call could not be made or completed
	// (invalid params, a refused host, a timeout, a network failure).
	IntegrationOutcomeFailed = "failed"
	// IntegrationOutcomeCanceled: the caller hung up; not a failure.
	IntegrationOutcomeCanceled = "canceled"
)
