package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func TestThreadAmbientMemoryExclusionMonotonicLineage(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("memory-chat", time.Now().UTC())))
	for _, item := range []struct{ id, parent string }{{"root", ""}, {"child", "root"}, {"grandchild", "child"}, {"sibling", ""}} {
		var parent *string
		if item.parent != "" {
			parent = convStr(item.parent)
		}
		_, err := env.Repo.CreateThread(ctx, &core.Thread{ID: item.id, ChatID: "memory-chat", ParentThreadID: parent})
		require.NoError(t, err)
	}
	excluded, err := env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "memory-chat", "root", false)
	require.NoError(t, err)
	require.False(t, excluded, "old/default rows preserve existing behavior")
	excluded, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "memory-chat", "root", true)
	require.NoError(t, err)
	require.True(t, excluded)
	for _, id := range []string{"root", "child", "grandchild"} {
		excluded, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "memory-chat", id, false)
		require.NoError(t, err)
		require.True(t, excluded, id)
		thread, err := env.Repo.GetThread(ctx, id)
		require.NoError(t, err)
		require.True(t, thread.ExcludeAmbientMemory, "policy must latch, not just be computed transiently")
	}
	excluded, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "memory-chat", "sibling", false)
	require.NoError(t, err)
	require.False(t, excluded, "unrelated fresh thread keeps defaults")
	_, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "other-chat", "root", true)
	require.Error(t, err, "a policy write is scoped to the chat")

	// Cross-chat forks are legitimate lineage, not a missing ancestor.
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("fork-chat", time.Now().UTC())))
	_, err = env.Repo.CreateThread(ctx, &core.Thread{ID: "cross-chat-fork", ChatID: "fork-chat", ParentThreadID: convStr("root")})
	require.NoError(t, err)
	excluded, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "fork-chat", "cross-chat-fork", false)
	require.NoError(t, err)
	require.True(t, excluded)

	// Once latched, a turn must not rewrite an already-true policy row.
	var before, after string
	require.NoError(t, env.ORM.Bun().NewRaw("SELECT xmin::text FROM threads WHERE id = 'root'").Scan(ctx, &before))
	excluded, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "memory-chat", "root", false)
	require.NoError(t, err)
	require.True(t, excluded)
	require.NoError(t, env.ORM.Bun().NewRaw("SELECT xmin::text FROM threads WHERE id = 'root'").Scan(ctx, &after))
	require.Equal(t, before, after, "already durable exclusions need no UPDATE")
}

func TestThreadAmbientMemoryExclusionDepthBoundary(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("depth-chat", time.Now().UTC())))
	_, err := env.ORM.Bun().NewRaw(`INSERT INTO threads
	 (id, chat_id, parent_thread_id, created_at, origin, exclude_ambient_memory)
	 SELECT 'depth-' || n, 'depth-chat', CASE WHEN n = 1 THEN NULL ELSE 'depth-' || (n - 1) END,
	 now(), 'node', n = 1 FROM generate_series(1, 257) n`).Exec(ctx)
	require.NoError(t, err)
	excluded, err := env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "depth-chat", "depth-256", false)
	require.NoError(t, err, "a root at the bound is still proved")
	require.True(t, excluded)
	_, err = env.Repo.ResolveThreadAmbientMemoryExclusion(ctx, "depth-chat", "depth-257", false)
	require.ErrorContains(t, err, "incomplete lineage", "even an excluded ancestor cannot hide traversal truncation")
}
