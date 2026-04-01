package db

import (
	"context"
	"database/sql"
)

type tokenCountStore interface {
	GetThreadTokenCountAtOrdinal(ctx context.Context, threadID string, contextSequence int64, maxOrdinal sql.NullInt64) (int64, error)
}

type sqlTokenCountStore struct {
	db   *WrappedDBTX
	bind func(string) string
}

func newSQLTokenCountStore(db *WrappedDBTX, bind func(string) string) tokenCountStore {
	return &sqlTokenCountStore{db: db, bind: bind}
}

func (s *sqlTokenCountStore) GetThreadTokenCountAtOrdinal(ctx context.Context, threadID string, contextSequence int64, maxOrdinal sql.NullInt64) (int64, error) {
	query := s.bind(`SELECT CAST(COALESCE(
		(
			SELECT COALESCE(m.token_count, 0)
			FROM messages m
			JOIN context_windows cw ON cw.id = m.context_window_id
			WHERE cw.thread_id = ? AND cw.sequence = ?
			  AND m.token_count IS NOT NULL
			  AND m.ordinal <= COALESCE(?, m.ordinal)
			ORDER BY m.ordinal DESC
			LIMIT 1
		), 0) AS BIGINT) AS total_tokens`)

	var tokens int64
	err := s.db.QueryRowContext(ctx, query, threadID, contextSequence, maxOrdinal).Scan(&tokens)
	return tokens, err
}
