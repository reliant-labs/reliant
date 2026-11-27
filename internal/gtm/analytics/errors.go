// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"errors"
	"strings"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// Errors. Each is a comparable value with fixed text (the gtm.Analytics
// contract): none carries the control plane's own error, which is logged
// instead, and none can be changed by an importer, who reaches them only
// through the accessors below and compares with errors.Is.
//
//   - The validation refusals are a *gtm.ValidationError to errors.As (so
//     gtm.ErrInvalidArgument), which ReadProductAnalytics passes through.
//   - Every control-plane failure wraps gtm.ErrAnalyticsFailed; a part of
//     the read that is not configured wraps gtm.ErrAnalyticsUnavailable.
//   - The client-construction errors are plain.
var (
	errNoOrg              = invalidError{field: "org", reason: "no org to read product analytics for"}
	errInvalidName        = invalidError{field: "name", reason: "an event or property name is not allowed"}
	errIdentifierProperty = invalidError{field: "property", reason: "property_breakdown cannot group by an identifier property"}
	errDuplicateStep      = invalidError{field: "events", reason: "a funnel's steps must be distinct events"}
	errWindowTooLong      = invalidError{field: "window", reason: "event_counts, event_trend and property_breakdown read at most 30 days"}

	errCredentialRefused = failedError{detail: "the control plane refused the analytics credential"}
	errNotSetUp          = failedError{detail: "the org's product analytics is not set up"}
	errQuotaExhausted    = failedError{detail: "the org's analytics query limit was reached; try again later"}
	errTimedOut          = failedError{detail: "the query timed out"}
	errQueryRefused      = failedError{detail: "the control plane refused the query"}
	errBadResponse       = failedError{detail: "the control plane's answer was not understood"}
	// errTokenNeedsDeployRead is ListEnvironments refusing the token: an
	// environment filter needs deploy:read beside obs:read. The read never
	// falls back to every environment.
	errTokenNeedsDeployRead = failedError{detail: "the analytics token needs the deploy:read scope to filter by environment"}
	errEnvLookupFailed      = failedError{detail: "the control plane's environments could not be listed"}

	// errEnvFilterUnavailable is a query naming an environment in a process
	// with no environment resolver (or no token for it).
	errEnvFilterUnavailable = unavailableError{detail: "filtering by environment is not configured"}

	errInsecureControlPlaneURL = clientError("analytics: the control-plane URL must be https (http only to a loopback host)")
	errUnsafeRedirect          = clientError("analytics: refused a control-plane redirect off https or to another host")
)

// ErrNoOrg is a query with no org to scope it to.
func ErrNoOrg() error { return errNoOrg }

// ErrInvalidName is an event or property name outside the allowed characters.
func ErrInvalidName() error { return errInvalidName }

// ErrIdentifierProperty is a property_breakdown over a property that names
// an identifier (a user, session, email, ...).
func ErrIdentifierProperty() error { return errIdentifierProperty }

// ErrDuplicateStep is a funnel naming an event twice.
func ErrDuplicateStep() error { return errDuplicateStep }

// ErrWindowTooLong is a sql template over more than MaxSQLWindow.
func ErrWindowTooLong() error { return errWindowTooLong }

// ErrCredentialRefused is the control plane refusing the analytics token
// (Unauthenticated or PermissionDenied).
func ErrCredentialRefused() error { return errCredentialRefused }

// ErrNotSetUp is an org whose observability is not set up
// (FailedPrecondition).
func ErrNotSetUp() error { return errNotSetUp }

// ErrQuotaExhausted is the org's hourly query quota or a query limit
// (ResourceExhausted).
func ErrQuotaExhausted() error { return errQuotaExhausted }

// ErrTimedOut is a query that ran out of time (DeadlineExceeded).
func ErrTimedOut() error { return errTimedOut }

// ErrQueryRefused is the control plane refusing the query itself
// (InvalidArgument, NotFound).
func ErrQueryRefused() error { return errQueryRefused }

// ErrBadResponse is an answer this package cannot read.
func ErrBadResponse() error { return errBadResponse }

// ErrTokenNeedsDeployRead is DeployService.ListEnvironments refusing the
// analytics token (PermissionDenied): it lacks deploy:read.
func ErrTokenNeedsDeployRead() error { return errTokenNeedsDeployRead }

// envNotQueryable is QueryPanel refusing a read over the ids an
// Environment resolved to (PermissionDenied or InvalidArgument), whose
// cached resolution was dropped. name is a validated environment name, so
// the text stays fixed but for it.
func envNotQueryable(name string) error {
	return failedError{detail: "environment " + name + " could not be queried; it may have been recreated, retry"}
}

// IsEnvNotQueryable reports whether err is the error a read returns when the
// control plane refused the environment it named.
func IsEnvNotQueryable(err error) bool {
	var f failedError
	return errors.As(err, &f) && strings.HasPrefix(f.detail, "environment ") && strings.HasSuffix(f.detail, "; it may have been recreated, retry")
}

// ErrEnvLookupFailed is any other ListEnvironments failure.
func ErrEnvLookupFailed() error { return errEnvLookupFailed }

// ErrEnvFilterUnavailable is an Environment filter with no resolver
// configured (gtm.ErrAnalyticsUnavailable).
func ErrEnvFilterUnavailable() error { return errEnvFilterUnavailable }

// ErrInsecureControlPlaneURL is a control-plane URL the analytics token
// would cross in the clear: anything but https, or http to a loopback host.
func ErrInsecureControlPlaneURL() error { return errInsecureControlPlaneURL }

// ErrUnsafeRedirect is a control-plane redirect the client refuses to follow.
func ErrUnsafeRedirect() error { return errUnsafeRedirect }

// invalidError is a validation refusal: "<field>: <reason>", like the
// domain's own. It is gtm.ErrInvalidArgument, and to errors.As a
// *gtm.ValidationError (a fresh copy, so the original cannot be changed
// through it).
type invalidError struct{ field, reason string }

func (e invalidError) Error() string { return e.field + ": " + e.reason }

func (e invalidError) Unwrap() error { return gtm.ErrInvalidArgument }

func (e invalidError) As(target any) bool {
	if ve, ok := target.(**gtm.ValidationError); ok {
		*ve = &gtm.ValidationError{Field: e.field, Reason: e.reason}
		return true
	}
	return false
}

// failedError is a control-plane failure: gtm.ErrAnalyticsFailed with a
// fixed detail.
type failedError struct{ detail string }

func (e failedError) Error() string { return gtm.ErrAnalyticsFailed.Error() + ": " + e.detail }

func (e failedError) Unwrap() error { return gtm.ErrAnalyticsFailed }

// unavailableError is a part of the read that is not configured:
// gtm.ErrAnalyticsUnavailable with a fixed detail.
type unavailableError struct{ detail string }

func (e unavailableError) Error() string {
	return gtm.ErrAnalyticsUnavailable.Error() + ": " + e.detail
}

func (e unavailableError) Unwrap() error { return gtm.ErrAnalyticsUnavailable }

// clientError is a refusal to build or follow a client connection.
type clientError string

func (e clientError) Error() string { return string(e) }
