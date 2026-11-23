package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// applyShareChange is how the sharing handler writes a visibility: the change
// and its share event in one ReadCommitted transaction.
func applyShareChange(ctx context.Context, repo *Repo, c core.ShareChange) (bool, error) {
	var changed bool
	err := repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		changed, err = store.ApplyShareChange(ctx, repo.ORM(), c)
		return err
	})
	return changed, err
}
