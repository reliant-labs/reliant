// Copyright (c) 2025 Reliant Labs

// Package purgetest builds an accountpurge.Purger over a test database, wired to the real
// credential packages the way internal/app wires it.
package purgetest

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/credentials/automationtokens"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/db"
)

// New returns the Purger for repo's database.
func New(tb testing.TB, repo *db.Repo) *accountpurge.Purger {
	tb.Helper()
	v := db.NewTestVault(tb, repo)
	tokens, err := automationtokens.New(automationtokens.Deps{DB: repo.ORM(), Sealer: v})
	if err != nil {
		tb.Fatalf("automationtokens.New: %v", err)
	}
	accounts, err := provideraccounts.New(provideraccounts.Deps{DB: repo.ORM(), Sealer: v})
	if err != nil {
		tb.Fatalf("provideraccounts.New: %v", err)
	}
	connections, err := connectionvault.New(connectionvault.Deps{DB: repo.ORM()})
	if err != nil {
		tb.Fatalf("connectionvault.New: %v", err)
	}
	p, err := accountpurge.New(accountpurge.Deps{DB: repo.ORM(), Automation: tokens, Accounts: accounts, Connections: connections})
	if err != nil {
		tb.Fatalf("accountpurge.New: %v", err)
	}
	return p
}
