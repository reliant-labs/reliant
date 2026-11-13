// Copyright (c) 2025 Reliant Labs

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The lifecycle of an org's credentials (control-plane
// docs/design/org-provider-accounts.md §6, P8). Two sweeps read these: one
// deletes grants that outlived the membership they were made in (§3.3), the
// other purges everything a gone org owned. Control-plane decides membership
// and whether an org is gone; these only read and delete reliant's rows.

// ListProviderAccountGrantMembers returns the distinct (org, member) pairs
// that hold provider_account_grants rows, after the pair after in (org_id,
// user_id) order, at most limit of them: one page of the stale-grant sweep.
// The zero pair starts from the beginning.
func ListProviderAccountGrantMembers(ctx context.Context, db orm.Context, after core.GrantMember, limit int) ([]core.GrantMember, error) {
	var rows []struct {
		OrgID  string `bun:"org_id"`
		UserID string `bun:"user_id"`
	}
	if err := db.Bun().NewRaw(`
SELECT DISTINCT org_id, user_id FROM provider_account_grants
WHERE (org_id, user_id) > (?0, ?1)
ORDER BY org_id, user_id
LIMIT ?2`, after.OrgID, after.UserID, limit).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list the members holding provider account grants: %w", err)
	}
	out := make([]core.GrantMember, len(rows))
	for i, r := range rows {
		out[i] = core.GrantMember{OrgID: r.OrgID, UserID: r.UserID}
	}
	return out, nil
}

// DeleteProviderAccountGrantsBefore deletes userID's grants in orgID made
// before before, and returns how many went. The stale-grant sweep passes the
// member's current membership start (a grant from an earlier membership is
// void), or, for a person who is no member, the moment it asked: a grant made
// after that answer may belong to a membership that began since, and is left
// for the next pass to judge.
func DeleteProviderAccountGrantsBefore(ctx context.Context, db orm.Context, orgID, userID string, before time.Time) (int64, error) {
	res, err := db.Bun().NewRaw(`
DELETE FROM provider_account_grants
WHERE org_id = ?0 AND user_id = ?1 AND granted_at < ?2`, orgID, userID, before.UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete stale provider account grants: %w", err)
	}
	return res.RowsAffected()
}

// ListOrgCredentialHolders returns the ids of the orgs reliant holds
// credentials or credential-bearing rows for, after the id after, in order,
// at most limit of them: one page of the gone-org sweep. A source here that
// PurgeOrgCredentials does not clear would be asked about forever, and a
// source PurgeOrgCredentials clears that is missing here would never be
// purged: TestOrgCredentialHoldersAreWhatThePurgeClears holds the two
// together.
func ListOrgCredentialHolders(ctx context.Context, db orm.Context, after string, limit int) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`
SELECT org_id FROM (
    SELECT org_id FROM provider_accounts WHERE owner_kind = 'org' AND org_id > ?0
    UNION SELECT attributed_org_id FROM provider_accounts WHERE attributed_org_id > ?0
    UNION SELECT org_id FROM provider_account_grants WHERE org_id > ?0
    UNION SELECT org_id FROM org_provider_workspaces WHERE org_id > ?0
    UNION SELECT org_id FROM connections WHERE owner_kind = 'org' AND org_id > ?0
    UNION SELECT org_id FROM oauth_flows WHERE org_id > ?0
    UNION SELECT tenant_id FROM vault_keys WHERE tenant_kind = 'org' AND tenant_id > ?0
) holders
ORDER BY org_id
LIMIT ?1`, after, limit).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list the orgs holding credentials: %w", err)
	}
	return ids, nil
}

// purgeOrgStep is one statement of PurgeOrgCredentials; ?0 is the org id.
type purgeOrgStep struct {
	name  string
	sql   string
	count func(*core.OrgCredentialsPurged) *int64
}

// purgeOrgSteps purge a gone org's credentials, in this order. Every
// statement is scoped by the org id, so a bug here can only under-delete,
// never reach another org's or a person's own credentials.
var purgeOrgSteps = []purgeOrgStep{
	// Its grants. They cascade with their accounts too; deleted first so
	// none outlives an account another statement missed.
	{"provider_account_grants", `DELETE FROM provider_account_grants WHERE org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.Grants }},
	// Its own accounts (the owner key 'org:<org>'). Their limits and thread
	// assignments cascade, so no thread stays on a destroyed key.
	{"provider_accounts", `DELETE FROM provider_accounts WHERE owner_kind = 'org' AND org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.ProviderAccounts }},
	// Members' managed Reliant keys minted in it: billed to its wallet, they
	// serve nothing now, and released they would pass for a personal key.
	{"provider_accounts_managed", `DELETE FROM provider_accounts
WHERE attributed_org_id = ?0 AND attribution_source = 'managed'`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.ManagedKeys }},
	// Members' seats it paid for: the login is the person's, so the row
	// stays, attributed to nobody (§6, "Org deleted").
	{"provider_accounts_attribution", `UPDATE provider_accounts
SET attributed_org_id = NULL, attribution_source = NULL, updated_at = NOW()
WHERE attributed_org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.Attributions }},
	{"org_provider_workspaces", `DELETE FROM org_provider_workspaces WHERE org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.Workspaces }},
	// Its connections (org-sharing §7): pending connects, then the audit
	// rows, then the rows. Secrets cascade; a trigger bound to one is
	// unbound (ON DELETE SET NULL).
	{"oauth_flows", `DELETE FROM oauth_flows WHERE org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.OAuthFlows }},
	{"connection_events", `DELETE FROM connection_events
WHERE connection_id IN (SELECT id FROM connections WHERE owner_kind = 'org' AND org_id = ?0)`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.ConnectionEvents }},
	{"connections", `DELETE FROM connections WHERE owner_kind = 'org' AND org_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.Connections }},
	// Last: dropping the org tenant's wrapped data keys is the crypto-shred.
	// Anything a statement above missed, and every backup, no longer opens.
	// Must follow every table holding data sealed under them
	// (connection_secrets references vault_keys).
	{"vault_keys", `DELETE FROM vault_keys WHERE tenant_kind = 'org' AND tenant_id = ?0`,
		func(p *core.OrgCredentialsPurged) *int64 { return &p.VaultKeys }},
}

// PurgeOrgCredentials removes every credential reliant holds for orgID, a gone
// org: its provider accounts, grants and workspace bindings, its connections,
// members' managed keys minted in it, and its vault tenant's keys; members'
// seats it paid for are released. It runs in the caller's transaction, so
// the purge is all or nothing, and is idempotent: a second run finds nothing.
// The caller decides the org is gone; nothing here asks.
func PurgeOrgCredentials(ctx context.Context, db orm.Context, orgID string) (core.OrgCredentialsPurged, error) {
	var out core.OrgCredentialsPurged
	if orgID == "" {
		// Every statement would match nothing, but an empty id is always a
		// caller's bug, and "purged nothing" would look like success.
		return out, errors.New("purge org credentials: empty org id")
	}
	for _, s := range purgeOrgSteps {
		res, err := db.Bun().NewRaw(s.sql, orgID).Exec(ctx)
		if err != nil {
			return core.OrgCredentialsPurged{}, fmt.Errorf("purge org credentials: %s: %w", s.name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return core.OrgCredentialsPurged{}, err
		}
		*s.count(&out) = n
	}
	return out, nil
}
