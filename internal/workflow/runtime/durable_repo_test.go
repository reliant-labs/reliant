package runtime

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// durableRepo is a real repository and its spawn tree as one durableResumeSource.
type durableRepo struct {
	*db.Repo
	store.ToolCallExtStore
}

func withSpawnTree(repo *db.Repo) durableRepo {
	return durableRepo{Repo: repo, ToolCallExtStore: store.NewToolCallExtStore(repo.ORM())}
}
