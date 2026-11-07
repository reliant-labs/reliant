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
}
