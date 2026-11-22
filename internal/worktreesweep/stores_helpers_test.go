package worktreesweep

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// stores is the sweep's store over a real repository, as the composition root
// wires it: the facade's shared reads and the worktree rows' ext store.
func stores(repo *db.Repo) Stores {
	return NewStores(repo, store.NewWorktreeExtStore(repo.ORM()))
}
