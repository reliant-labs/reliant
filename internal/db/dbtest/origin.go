// Copyright (c) 2025 Reliant Labs

package dbtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// SetChatOrigin stamps chats.origin the way the launcher does at birth, for a
// test that creates its chat with repo.CreateChat (origin USER) and records
// the chat's launch event by hand. Production never rewrites an origin.
func SetChatOrigin(t testing.TB, repo *db.Repo, chatID string, origin db.ChatOrigin) {
	t.Helper()
	res, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET origin = ? WHERE id = ?`, int32(origin), chatID).
		Exec(context.Background())
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "no chat %s to stamp", chatID)
}
