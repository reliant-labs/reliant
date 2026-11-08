package store

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// A query ported from sqlc with its $N placeholders left in.
const saveMessageSQL = `
INSERT INTO messages (id, chat_id)
VALUES ($1, $2)`

func SaveMessage(ctx context.Context, db orm.Context, id, chatID string) error {
	_, err := db.Bun().NewRaw(saveMessageSQL, id, chatID).Exec(ctx)
	return err
}

func Dispatch(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().ExecContext(ctx, "UPDATE run_events SET dispatched_at = now() WHERE id = $1", id)
	return err
}

func HasKey(ctx context.Context, db orm.Context, key string) (bool, error) {
	return db.Bun().NewSelect().Table("run_events").
		Where("payload ? 'tool' AND id = ?", key).
		Exists(ctx)
}

func AnyKey(ctx context.Context, db orm.Context, keys []string, id string) error {
	q := db.Bun().NewSelect().Table("run_events")
	q.Where("payload ?| ? AND id = ?", keys, id)
	return q.Scan(ctx)
}

func ByChat(chatID string) func(*bun.SelectQuery) {
	return func(q *bun.SelectQuery) {
		q.Where("chat_id = ? -- why?", chatID)
	}
}

func Literal(ctx context.Context, db orm.Context, id string) error {
	return db.Bun().NewRaw("SELECT 'what?' AS label, ? AS id", id).Scan(ctx)
}

func Safe(id string) bun.QueryWithArgs {
	return bun.SafeQuery("id = $1", id)
}
