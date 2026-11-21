package store

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Bun's own dialect: `?` and `?N`.
const saveMessageSQL = `
INSERT INTO messages (id, chat_id)
VALUES (?0, ?1)`

func SaveMessage(ctx context.Context, db orm.Context, id, chatID string) error {
	_, err := db.Bun().NewRaw(saveMessageSQL, id, chatID).Exec(ctx)
	return err
}

// The raw passthrough takes native $N — it never goes through bun.
func Dispatch(ctx context.Context, db orm.Context, id string) (sql.Result, error) {
	return db.Exec(ctx, "UPDATE run_events SET dispatched_at = now() WHERE id = $1 AND payload ? 'tool'", id)
}

// Escaped jsonb operators and question marks.
func HasKey(ctx context.Context, db orm.Context, key, id string) (bool, error) {
	return db.Bun().NewSelect().Table("run_events").
		Where(`payload \? 'tool' AND payload \?| ? AND note <> 'why\?' AND id = ?`, []string{key}, id).
		Exists(ctx)
}

// `? ?` is two placeholders: an identifier and a direction.
func OrderBy(col, dir string) func(*bun.SelectQuery) {
	return func(q *bun.SelectQuery) {
		q.OrderExpr("? ?", bun.Ident(col), bun.Safe(dir))
	}
}

// With no arguments bun leaves a `?` in place, so nothing can misbind.
func NoArgs(ctx context.Context, db orm.Context) error {
	_, err := db.Bun().NewRaw("SELECT count(*) FROM run_events WHERE payload ? 'tool' -- any?").Exec(ctx)
	return err
}

// $N inside a SQL string literal or a dollar-quoted body is text.
func Function(ctx context.Context, db orm.Context) error {
	_, err := db.Bun().NewRaw(`CREATE FUNCTION add1(int) RETURNS int AS $$ SELECT $1 + 1 $$ LANGUAGE sql`).Exec(ctx)
	return err
}

// Not bun: an unrelated Where on something else.
type gormish struct{}

func (gormish) Where(string, ...any) gormish { return gormish{} }

func Other(g gormish) { g.Where("id = $1", 1) }
