package sharing

import (
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// realDeps is the Deps the composition root builds, over repo's database.
func realDeps(repo *db.Repo, orgs OrgResolver) Deps {
	o := repo.ORM()
	return Deps{
		Logger:   slog.Default(),
		Config:   &typedconfig.Config{},
		Chats:    store.NewChatsWithActivityExtStore(o),
		Projects: store.NewProjectExtStore(o),
		// The Repo reads drafts through the same workflowdrafts service
		// composition wires.
		WorkflowDrafts: repo,
		Shares:         store.NewShareEventExtStore(o),
		Members:        store.NewChatMemberExtStore(o),
		Tx:             o,
		Orgs:           orgs,
	}
}
