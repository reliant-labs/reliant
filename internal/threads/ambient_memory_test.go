package threads

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

func TestAmbientMemoryExclusionAtomicThreadInitialization(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()
	create := func(id string, parent *string, requested bool) *db.Thread {
		t.Helper()
		_, thread, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: &db.Workflow{ID: "wf-" + id, ChatID: h.chatID, Thread: id, WorkflowName: "test", Status: db.Active(), CreatedAt: time.Now().UTC()},
			ChatID:   h.chatID, ThreadID: id, ParentThread: parent, ExcludeAmbientMemory: requested,
		})
		require.NoError(t, err)
		return thread
	}
	root := create("excluded-root", nil, true)
	require.True(t, root.ExcludeAmbientMemory)
	child := create("unstarted-child", &root.ID, false)
	require.True(t, child.ExcludeAmbientMemory, "must persist before first model turn, including a crash immediately after initialization")
	stored, err := store.GetThread(ctx, h.repo.ORM(), child.ID)
	require.NoError(t, err)
	require.True(t, stored.ExcludeAmbientMemory)
	require.True(t, create("unstarted-child", &root.ID, false).ExcludeAmbientMemory, "idempotent resume cannot reset policy")
	require.False(t, create("ordinary-root", nil, false).ExcludeAmbientMemory)
}
