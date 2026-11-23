// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"github.com/reliant-labs/reliant/internal/db"
)

// Stores is the production store of the trigger package: the repository
// facade for the reads other areas still share, joined with the generated
// stores this package owns.
type Stores struct {
	*db.Repo
	CapacityStores
}

// NewStores joins the repository facade with the generated trigger stores.
func NewStores(repo *db.Repo) Stores {
	if repo == nil {
		panic("triggers: NewStores requires a repository")
	}
	return Stores{Repo: repo, CapacityStores: NewCapacityStores(repo.ORM())}
}

var (
	_ FireRepo      = Stores{}
	_ EventFireRepo = Stores{}
)
