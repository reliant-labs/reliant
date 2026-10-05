// Copyright (c) 2025 Reliant Labs
package core

import "time"

// IntegrationAccessGrant is one (account, resource) a user's own provider
// credential can see: for GitHub, a repository in an App installation.
// Access-gated routing sends an event about that resource only to triggers
// whose owner holds a fresh grant for it.
type IntegrationAccessGrant struct {
	// AccountKey is the provider account the access is through (the GitHub
	// installation id), the same value providers report as Event.AccountKey.
	AccountKey string
	// ResourceKey is the resource (the GitHub repository id), the same value
	// providers report as Event.ResourceKey.
	ResourceKey string
	// ResourceLabel is a readable name (owner/name), for display only.
	ResourceLabel string
}

// IntegrationAccessRevocation is the provider telling us access ended. Empty
// fields are wildcards; at least AccountKey or SubjectID must be set, so a
// revocation can never wipe every user's access.
type IntegrationAccessRevocation struct {
	AccountKey  string
	ResourceKey string
	// SubjectID is the user's id AT THE PROVIDER (the GitHub user id): a
	// member removed from an org loses exactly their own grants.
	SubjectID string
}

// IntegrationAccessRefresh is the refresh bookkeeping for one (user,
// integration).
type IntegrationAccessRefresh struct {
	UserID        string
	IntegrationID string
	// RefreshedAt is the last SUCCESSFUL refresh; nil when none succeeded.
	RefreshedAt   *time.Time
	LastError     string
	LastAttemptAt *time.Time
}
