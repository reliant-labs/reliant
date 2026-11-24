package sharing

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// realDeps is the Deps the composition root builds, over repo's database.
func realDeps(repo *db.Repo, orgs OrgResolver) Deps {
	o := repo.ORM()
	return Deps{
		Chats:    store.NewChatsWithActivityExtStore(o),
		Projects: store.NewProjectExtStore(o),
		Shares:   store.NewShareEventExtStore(o),
		Members:  store.NewChatMemberExtStore(o),
		Tx:       o,
		Orgs:     orgs,
	}
}
