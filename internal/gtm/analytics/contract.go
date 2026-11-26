// Copyright (c) 2026 Reliant Labs

// Package analytics is gtm.Analytics over the control plane's
// ObservabilityService.QueryPanel: GTM's product-analytics read (plan B.5).
//
// forge:outbound-io
//
// It is the outbound adapter to the control plane's ObservabilityService. It
// reads product events only, of the one org it is handed, with an
// owner-minted, org-bound control-plane access token scoped obs:read (the
// secret GtmConfig.analytics_token). A browser session is never the
// credential: a scheduled run has none (internal/changetools/controller.go).
//
// Every query is built here from a fixed template: event_counts, event_trend
// and property_breakdown are sql panels over product_events (templates.go),
// funnel is the control plane's own funnel panel. The caller never supplies
// SQL, names reach a statement only as escaped literals after validation, and
// every answer is aggregates: no user id or other identifier is selected or
// returned. Control-plane errors are logged and returned as fixed messages.
//
// Its contract is the consumer's, gtm.Analytics: Service embeds it and adds
// nothing, so the two cannot drift. internal/app builds it by hand (New, with
// the token and the control plane's URL); with no token every query is
// gtm.ErrAnalyticsUnavailable.
//
//forge:provided
package analytics

import "github.com/reliant-labs/reliant/internal/gtm"

// Service is the consumer's contract, gtm.Analytics.
type Service interface {
	gtm.Analytics
}
