// Copyright (c) 2025 Reliant Labs
package drivers

// Scope is whose provider accounts a resolution may draw on: the person the
// call acts as, the org whose work it is, and whether a person is behind it.
//
// Every read of the candidate accounts takes one (BuildAvailableDrivers, the
// lookups over it, GetDriver), so no code path reaches an account without
// naming whose work it is. This is the seam of control-plane
// docs/design/org-provider-accounts.md §4.1, shared with org-policies R2: org
// keys, org-paid seats and the org's provider policy (providers.allowed,
// allowed_sources) are all decided by the one loader beneath it from these
// fields, never by a second loader or a per-call-site filter.
//
// Today the loader reads the person's own accounts whatever the scope names:
// no org-owned or org-attributed account exists yet, so every scope resolves
// exactly as before.
type Scope struct {
	// UserID is the person the call acts as. Required.
	UserID string
	// OrgID is the org whose work the call is: the chat's or the trigger's org,
	// read by the server from that row, never from workflow inputs, a client
	// or a daemon. Empty for work that belongs to no org: a one-shot call with
	// no run, or a chat that predates orgs.
	OrgID string
	// Attended reports that a person is behind the run. An unattended run (a
	// stored trigger fired) may spend only org-funded accounts (§11).
	Attended bool
}

// PersonalScope is the scope of a call that belongs to no run: key
// validation, a probe, the model picker outside a chat. It reaches only the
// person's own accounts, never an org's account or a seat attributed to an
// org, whatever any org's policy says. A person is acting, so it is attended.
func PersonalScope(userID string) Scope {
	return Scope{UserID: userID, Attended: true}
}

// Personal reports that the scope names no org: only the person's own
// accounts are candidates.
func (s Scope) Personal() bool { return s.OrgID == "" }
