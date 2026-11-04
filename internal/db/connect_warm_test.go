// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// database/sql dials a connection only when a caller finds none idle, so that
// caller waits for the dial — measured on chat opens in prod as 85-336ms
// db.connect spans. warmIdle dials them ahead, off the request path, and
// leaves them idle; it never takes more than fits beside the connections in
// use, and stops once the pool is closed.
func TestWarmIdle_DialsIdleConnectionsAheadOfUse(t *testing.T) {
	t.Parallel()

	_, dsn := NewTestDatabase(t)
	pool, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	pool.SetMaxOpenConns(6)
	pool.SetMaxIdleConns(4)

	require.Equal(t, 0, pool.Stats().OpenConnections)
	require.True(t, warmIdle(pool, 4))
	require.Equal(t, 4, pool.Stats().Idle, "the idle set is topped up to n")

	// Two in use: only what fits beside them is dialled, never a connection
	// a request could need.
	a, err := pool.Conn(t.Context())
	require.NoError(t, err)
	b, err := pool.Conn(t.Context())
	require.NoError(t, err)
	require.True(t, warmIdle(pool, 4))
	require.Equal(t, 6, pool.Stats().OpenConnections)
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	require.NoError(t, pool.Close())
	require.False(t, warmIdle(pool, 4), "a closed pool ends the warmer")
}
