// Copyright (c) 2025 Reliant Labs

// Package authmetrics holds the user audience's authentication counter. It is
// its own package, importing nothing of reliant's, so internal/auth can count
// what it verifies and internal/observability can register the counter
// without the two importing each other.
//
//forge:exclude-contract: a process-global Prometheus collector, as internal/observability's are
package authmetrics

import "github.com/prometheus/client_golang/prometheus"

// CredentialsTotal counts the user audience's authenticated calls by the
// Credential they were made with (internal/auth Credential.Label:
// supabase_session, oauth_client, access_token, shared_key, identity_token,
// browser_session), the validator that proved it (source: supabase,
// dev_signer, identity, shared_key, access_token) and the channel it arrived
// on (authorization, cookie, unknown). It is the evidence that Supabase
// traffic has stopped before its validator is removed (design-auth-swap §3).
// Every label value is one of those fixed names.
var CredentialsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "reliant",
		Subsystem: "auth",
		Name:      "credentials_total",
		Help:      "Authenticated user calls by credential kind, the validator that proved it (source) and the channel it arrived on.",
	},
	[]string{"credential", "source", "channel"},
)
