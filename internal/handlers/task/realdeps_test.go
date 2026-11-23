package task

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// RealDeps is the Deps the composition root builds, over repo's database:
// the generated stores, with repo itself telling clients to refetch.
func RealDeps(repo *db.Repo) Deps {
	o := repo.ORM()
	return Deps{
		Tasks:   store.NewTaskExtStore(o),
		Plans:   store.NewPlanExtStore(o),
		Threads: store.NewThreadExtStore(o),
		Chats:   store.NewChatExtStore(o),
		Refetch: repo,
		Tx:      o,
	}
}
