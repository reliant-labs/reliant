package attachment

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// RealDeps is the Deps the composition root builds, over repo's database.
func RealDeps(repo *db.Repo) Deps {
	return Deps{Attachments: store.NewAttachmentExtStore(repo.ORM()), DB: repo.ORM()}
}
