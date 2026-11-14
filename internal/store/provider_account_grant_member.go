// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// ListMemberAccountGrants returns, for one member of one org, when each of the org's accounts was
// granted to them (provider_account_grants), keyed by account id. Whether a grant still counts
// (it must postdate the member's current membership) is the caller's rule, not this query's.
func ListMemberAccountGrants(ctx context.Context, db orm.Context, orgID, userID string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if orgID == "" || userID == "" {
		return out, nil
	}
	rows, err := ListProviderAccountGrant(ctx, db,
		orm.WhereEq(ProviderAccountGrantFieldOrgID, orgID),
		orm.WhereEq(ProviderAccountGrantFieldUserID, userID))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.AccountID] = r.GrantedAt
	}
	return out, nil
}
