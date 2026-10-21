// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for CommandFavorite. The generated command_favorite_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: commandFavoriteRepo (a *crud.Repo[CommandFavorite]) and the
//     package-level GetCommandFavoriteByID / ListCommandFavorite / CreateCommandFavorite / UpdateCommandFavorite / DeleteCommandFavorite
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     CommandFavorite struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindCommandFavoriteBy(ctx context.Context, db orm.Context, /* args */) ([]*CommandFavorite, error) {
//		var rows []*CommandFavorite
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveCommandFavorites(ctx context.Context, db orm.Context) ([]*CommandFavorite, error) {
//		return ListCommandFavorite(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
)

// CommandFavoriteExtra carries fields on CommandFavorite that are NOT columns — computed
// values, transient state, anything derived rather than stored. CommandFavorite is
// embedded on CommandFavoriteExtra (see command_favorite_orm.go), so a field added here is a field on
// every CommandFavorite value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type CommandFavoriteExtra struct {
}

// ListCommandFavorites returns the command keys the user starred in the
// project, oldest first.
func ListCommandFavorites(ctx context.Context, db orm.Context, userID, projectID string) ([]string, error) {
	rows, err := ListCommandFavorite(ctx, db,
		orm.WhereEq(CommandFavoriteFieldUserID, userID),
		orm.WhereEq(CommandFavoriteFieldProjectID, projectID),
		orm.WithOrderBy(CommandFavoriteFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list command favorites: %w", err)
	}
	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = row.CommandKey
	}
	return keys, nil
}

// AddCommandFavorite stars a command; starring one already starred is a
// no-op.
func AddCommandFavorite(ctx context.Context, db orm.Context, userID, projectID, commandKey string) error {
	row := &CommandFavorite{
		ID: uuid.New().String(), UserID: userID, ProjectID: projectID, CommandKey: commandKey,
	}
	_, err := db.Bun().NewInsert().Model(row).
		Value(CommandFavoriteFieldCreatedAt, "NOW()").
		On("CONFLICT (user_id, project_id, command_key) DO NOTHING").
		Exec(ctx)
	return err
}

// RemoveCommandFavorite unstars a command; one not starred is a no-op.
func RemoveCommandFavorite(ctx context.Context, db orm.Context, userID, projectID, commandKey string) error {
	_, err := db.Bun().NewDelete().Model((*CommandFavorite)(nil)).
		Where("user_id = ?", userID).
		Where("project_id = ?", projectID).
		Where("command_key = ?", commandKey).
		Exec(ctx)
	return err
}
