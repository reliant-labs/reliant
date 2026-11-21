package devidp

import (
	"context"
	"fmt"
	"strings"
)

// UnprovenAccount is an account whose address is unverified and that holds
// a credential: created before no-credential-before-proof was the rule
// (forge#2121), by the legacy or no-mail sign-up (ContactOnly), or by hand.
// The broker never proves into one (its owner gets a fresh account), and
// under a verified-email policy none can sign in.
type UnprovenAccount struct {
	UserID string
	Email  string
	// Credentials are the sign-in methods it holds, as the issuer names them
	// (AUTHENTICATION_METHOD_TYPE_PASSWORD, _PASSKEY, _IDP, _TOTP, …).
	Credentials []string
	// ContactOnly: created where no mail could prove the address
	// (MetaContactOnly). Its holder's password is their working one where
	// the policy does not require a verified address.
	ContactOnly bool
	// SupersededBy: a fresh account took its address over (the address it
	// had is in MetaSupersededEmail).
	SupersededBy string
}

// UnprovenAccounts lists every account that is unverified and holds a
// credential. READ-ONLY: it changes nothing, so it is safe to run against a
// live instance to decide what, if anything, to clean up. It costs one
// search page per 100 accounts plus a read or two per unverified one.
//
// The broker's credential may run it (it reads users, their sign-in methods
// and their metadata).
func (c *Client) UnprovenAccounts(ctx context.Context) ([]UnprovenAccount, error) {
	hits, err := searchAll[humanUser](ctx, c, "/v2/users", map[string]any{
		"queries": []any{map[string]any{"typeQuery": map[string]any{"type": "TYPE_HUMAN"}}},
	}, "result", true)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var out []UnprovenAccount
	for _, h := range hits {
		u, ok := h.user()
		if !ok || u.EmailVerified {
			continue
		}
		kinds, err := c.credentialKinds(ctx, u.ID)
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", u.ID, err)
		}
		if len(kinds) == 0 {
			continue
		}
		a := UnprovenAccount{UserID: u.ID, Email: u.Email, Credentials: kinds}
		md, err := c.MetadataWithPrefix(ctx, u.ID, "forge.")
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", u.ID, err)
		}
		a.ContactOnly = md[MetaContactOnly] != ""
		a.SupersededBy = strings.TrimSpace(md[MetaSupersededBy])
		out = append(out, a)
	}
	return out, nil
}
