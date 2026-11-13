// Copyright (c) 2025 Reliant Labs

package core

// The lifecycle of an org's credentials in reliant (control-plane
// docs/design/org-provider-accounts.md §6, P8): grants that outlived the
// membership they were made in, and everything an org owned once the org is
// gone. Membership and org existence are control-plane's; reliant asks and
// sweeps.

// GrantMember is one (org, member) pair that holds provider_account_grants
// rows: the unit the stale-grant sweep asks control-plane about.
type GrantMember struct {
	OrgID  string
	UserID string
}

// OrgCredentialsPurged counts what purging one gone org's credentials
// removed (or, for Attributions, released).
type OrgCredentialsPurged struct {
	// ProviderAccounts are the org's own accounts (owner_kind 'org'); their
	// grants, limits and thread assignments cascade.
	ProviderAccounts int64
	// Grants are the org's provider_account_grants rows.
	Grants int64
	// Workspaces are the org's team-workspace bindings.
	Workspaces int64
	// Attributions are members' seats the org paid for, released: the row
	// stays the person's, attributed to nobody.
	Attributions int64
	// ManagedKeys are members' managed Reliant keys minted in the org,
	// deleted: a key billed to a gone org's wallet serves nothing, and
	// releasing it would make it look personal.
	ManagedKeys int64
	// Connections are the org's connections; their secrets cascade.
	Connections int64
	// ConnectionEvents are the audit rows of the org's connections.
	ConnectionEvents int64
	// OAuthFlows are pending connects of an org connection.
	OAuthFlows int64
	// VaultKeys are the org tenant's wrapped data keys: deleting them is the
	// crypto-shred.
	VaultKeys int64
	// SeatReleases are the org's records of seats its admins released.
	SeatReleases int64
}

// Total is every row the purge touched.
func (p OrgCredentialsPurged) Total() int64 {
	return p.ProviderAccounts + p.Grants + p.Workspaces + p.Attributions + p.ManagedKeys +
		p.Connections + p.ConnectionEvents + p.OAuthFlows + p.VaultKeys + p.SeatReleases
}
