package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// SetDaemonRestartStatus writes only for the stream that owns the lease.
// The identity retains it after lease release; a new process clears it on
// registration. An old stream's delayed drained report cannot taint its replacement.
func SetDaemonRestartStatus(ctx context.Context, db orm.Context, userID, daemonID string, attachedAt time.Time, status string) error {
	return writeDaemonRestartStatus(ctx, db, userID, daemonID, attachedAt, status, nil)
}

// CompareDaemonRestartStatus fences restoration/clearing against the exact
// intent read, in addition to the owning attachment. A racing accepted request
// cannot be cleared by an older registration snapshot.
func CompareDaemonRestartStatus(ctx context.Context, db orm.Context, userID, daemonID string, attachedAt time.Time, previous, status string) error {
	return writeDaemonRestartStatus(ctx, db, userID, daemonID, attachedAt, status, &previous)
}

func writeDaemonRestartStatus(ctx context.Context, db orm.Context, userID, daemonID string, attachedAt time.Time, status string, previous *string) error {
	// Lock order is attachment -> daemon, matching ReleaseDaemonAttachment's
	// DELETE -> UPDATE. Upsert/renew/reap already take the attachment tuple
	// lock. Locking the matching generation makes validation and mutation one
	// serialized operation; an MVCC EXISTS snapshot alone cannot fence them.
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var generation time.Time
		if err := db.QueryRow(ctx, `SELECT attached_at FROM daemon_attachment
 WHERE daemon_id = $1 AND user_id = $2 AND attached_at = $3
 FOR UPDATE`, daemonID, userID, attachedAt).Scan(&generation); err != nil {
			return err
		}
		return updateDaemonRestartStatus(ctx, db, userID, daemonID, attachedAt, status, previous)
	})
}

func updateDaemonRestartStatus(ctx context.Context, db orm.Context, userID, daemonID string, attachedAt time.Time, status string, previous *string) error {
	var value any
	if status != "" {
		value = status
	}
	var expected any
	if previous != nil && *previous != "" {
		expected = *previous
	}
	res, err := db.Exec(ctx, `UPDATE daemons SET restart_status = $4::jsonb
 WHERE id = $1 AND user_id = $2 AND EXISTS (
 SELECT 1 FROM daemon_attachment a WHERE a.daemon_id = daemons.id
 AND a.user_id = daemons.user_id AND a.attached_at = $3)
 AND (NOT $5 OR restart_status IS NOT DISTINCT FROM $6::jsonb)`, daemonID, userID, attachedAt, value, previous != nil, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
