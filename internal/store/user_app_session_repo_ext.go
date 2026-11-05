package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

// RecordFirstAppSession records that userID has made an authenticated request,
// and reports whether this call is the first one ever: the INSERT that wins
// (user_app_sessions, one row per user). A concurrent call on another replica
// inserts nothing and reports false, so exactly one caller sees true.
func RecordFirstAppSession(ctx context.Context, db orm.Context, userID string) (bool, error) {
	result, err := db.Bun().NewRaw(`
 INSERT INTO user_app_sessions (user_id) VALUES (?)
 ON CONFLICT (user_id) DO NOTHING`, userID).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
