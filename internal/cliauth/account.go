package cliauth

import (
	"strings"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// Account is who a login signed in as, as the control plane named it in the
// token reply: the external id (the IdP subject — the same id the daemon
// gateway names a daemon credential's owner by) and the account email.
//
// A CLI login keeps it on its forge credential (credentials.Credential's
// AccountID / AccountEmail), beside the token it describes; AccountOf reads it
// back. It is zero when the control plane did not say: one that predates the
// account_id / account_email reply fields, or a login saved before this
// machine recorded them. Every caller treats zero as "unknown" and behaves as
// it did before accounts were recorded — never as a reason to fail.
type Account struct {
	ID    string `json:"account_id,omitempty"`
	Email string `json:"account_email,omitempty"`
}

// Known reports whether the account was named.
func (a Account) Known() bool { return strings.TrimSpace(a.ID) != "" }

// Same reports whether a and b are known to be one account. Two unknowns are
// not the same: nothing says they are.
func (a Account) Same(b Account) bool { return a.Known() && b.Known() && a.ID == b.ID }

// String is how a person reads the account: the email, else the id, else "".
func (a Account) String() string {
	if e := strings.TrimSpace(a.Email); e != "" {
		return e
	}
	if a.Known() {
		return "account " + a.ID
	}
	return ""
}

// AccountOf is the account login c signed in as; zero when unknown.
func AccountOf(c credentials.Credential) Account {
	return Account{ID: c.AccountID, Email: c.AccountEmail}
}

// accountFromToken reads the control plane's account_id / account_email
// token-reply extension fields; zero when it sent none.
func accountFromToken(tok *oauth2.Token) Account {
	if tok == nil {
		return Account{}
	}
	var a Account
	a.ID, _ = tok.Raw["account_id"].(string)
	a.Email, _ = tok.Raw["account_email"].(string)
	return a
}
