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
// ── ENVIRONMENTS ─────────────────────────────────────────────────────────
// AnalyticsQuery.Environment names a deploy environment ("prod"). An
// EnvResolver (NewEnvResolver) maps it, through control-plane
// DeployService.ListEnvironments with the same analytics token, to every
// live environment of the org with that name (one per project, plus the
// project-less one), or only ProjectID's; the ids go in QueryScope.env_ids
// (and the project in project_id) of the panel query, and in
// AnalyticsResult.EnvironmentIDs. Resolutions are cached per (org, name,
// project) for DefaultEnvCacheTTL. The token must then carry deploy:read
// beside obs:read; without it the read fails (ErrTokenNeedsDeployRead), and
// it never falls back to every environment. An unknown name is a
// *gtm.ValidationError listing the names there are.
//
// With no Environment the read is AllEnvironments, and that view has
// caveats: it includes events with no environment id and events from
// deleted environments (so it can exceed the sum of the per-environment
// reads), and it includes preview and local environments. A named read
// excludes all of those except preview or local environments that carry
// the name.
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
